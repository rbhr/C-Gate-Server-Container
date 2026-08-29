package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

//go:embed console.html
var consoleHTML embed.FS

const (
	cgateHost        = "localhost"
	cgateCommandPort = "20023"
	cgateEventPort   = "20024"
	cgateStatusPort  = "20025"
	listenAddr       = ":8980"

	// TCP keepalive interval for long-lived connections
	keepAliveInterval = 30 * time.Second

	// How long a single dial attempt gets before it is given up on.
	dialTimeout = 5 * time.Second

	// How long to wait before dialling again after a failed attempt.
	dialRetryInterval = 3 * time.Second

	// Per-line read deadline for a command.
	commandReadDeadline = 5 * time.Second

	// How often to send a heartbeat on the command connection to keep
	// it alive through NAT/firewalls and detect silent drops.
	commandHeartbeat = 2 * time.Minute
)

// wsHub manages WebSocket clients
type wsHub struct {
	mu      sync.RWMutex
	clients map[*websocket.Conn]bool
}

func newHub() *wsHub {
	return &wsHub{clients: make(map[*websocket.Conn]bool)}
}

func (h *wsHub) add(ws *websocket.Conn) {
	h.mu.Lock()
	h.clients[ws] = true
	h.mu.Unlock()
}

func (h *wsHub) remove(ws *websocket.Conn) {
	h.mu.Lock()
	_, ok := h.clients[ws]
	delete(h.clients, ws)
	h.mu.Unlock()
	if ok {
		ws.Close()
	}
}

func (h *wsHub) broadcast(msg map[string]string) {
	data, _ := json.Marshal(msg)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ws := range h.clients {
		if _, err := ws.Write(data); err != nil {
			go h.remove(ws)
		}
	}
}

var hub = newHub()

// Connection state for the health/ready endpoints. Written by the goroutines
// owning each connection, read by HTTP handlers, so these are atomic rather
// than guarded by the command session's mutex — a readiness probe must not
// block behind an in-flight command.
var (
	eventStreamUp  atomic.Bool
	statusStreamUp atomic.Bool
	commandUp      atomic.Bool
)

// dialCGate makes one attempt to connect to a C-Gate port, enabling TCP
// keepalive on success. It reports failure rather than retrying, so a caller
// holding a lock can bound how long it is prepared to wait.
func dialCGate(port string) (net.Conn, error) {
	addr := net.JoinHostPort(cgateHost, port)
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return nil, err
	}
	// Enable TCP keepalive so the OS detects dead connections
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(keepAliveInterval)
	}
	log.Printf("Connected to C-Gate %s", addr)
	return conn, nil
}

// dialCGateForever retries until it connects. Only safe on a goroutine that
// blocks nothing but itself — the stream readers qualify, the command session
// does not.
func dialCGateForever(port string) net.Conn {
	addr := net.JoinHostPort(cgateHost, port)
	for {
		conn, err := dialCGate(port)
		if err == nil {
			return conn
		}
		log.Printf("Waiting for C-Gate on %s: %v", addr, err)
		time.Sleep(dialRetryInterval)
	}
}

// streamPort reads lines from a C-Gate port and broadcasts them.
// Reconnects automatically when the connection drops.
//
// There is deliberately no read deadline on these connections. Both ends live
// in the same container (cgateHost is localhost), so "peer died without
// closing the socket" is not a failure mode reachable here — if C-Gate exits,
// the read returns EOF/RST straight away and the reconnect below handles it.
// A deadline could therefore only ever fire on a healthy but quiet port, and
// both ports are legitimately quiet: the event interface emits nothing at the
// default global-event-level, and the status interface goes silent on an idle
// site. Tearing the connection down in that case cost a reconnect every
// deadline period and dropped any line arriving during it. TCP keepalive (see
// dialCGate) stays as the backstop for the connection genuinely going away.
func streamPort(port, streamName string, up *atomic.Bool) {
	for {
		conn := dialCGateForever(port)
		up.Store(true)
		scanner := bufio.NewScanner(conn)
		// C-Gate lines are short, but don't let one unusually long line kill
		// the stream with ErrTooLong and send us into a reconnect loop.
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			hub.broadcast(map[string]string{
				"stream": streamName,
				"data":   scanner.Text(),
				"time":   time.Now().Format("15:04:05"),
			})
		}
		up.Store(false)
		if err := scanner.Err(); err != nil {
			log.Printf("Stream %s connection lost: %v — reconnecting", streamName, err)
		} else {
			log.Printf("Stream %s closed by C-Gate (EOF) — reconnecting", streamName)
		}
		conn.Close()
		time.Sleep(2 * time.Second)
	}
}

// commandSession holds the persistent command connection and its reader
type commandSession struct {
	mu     sync.Mutex
	conn   net.Conn
	reader *bufio.Reader
}

var cmdSession = &commandSession{}

// connect dials the command port and drains C-Gate's banner.
//
// One attempt, reporting failure rather than retrying: this runs with s.mu
// held, and waiting there is what used to hang the bridge. maintain() does the
// waiting instead.
func (s *commandSession) connect() error {
	log.Printf("Command session: connecting to C-Gate command port %s", cgateCommandPort)
	conn, err := dialCGate(cgateCommandPort)
	if err != nil {
		return fmt.Errorf("connecting to C-Gate command port: %w", err)
	}
	s.conn = conn
	s.reader = bufio.NewReader(conn)
	// Drain the connect banner
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		if _, err := s.reader.ReadString('\n'); err != nil {
			break
		}
	}
	conn.SetReadDeadline(time.Time{})
	commandUp.Store(true)
	log.Printf("Command session: ready")
	return nil
}

// drop closes the command connection and marks the session down, leaving
// maintain() to rebuild it.
//
// Redialling here instead — which is what reconnect() used to do — means
// dialling with s.mu held. C-Gate is routinely down for minutes at a time (a
// restart, a project reload, a reboot) and the dial retried forever, so every
// /cgate request queued behind it for the whole outage while /health went on
// reporting the bridge as serving. Failing the request and letting a
// background goroutine wait is what keeps an outage off the HTTP path.
//
// Must be called with s.mu held.
func (s *commandSession) drop() {
	commandUp.Store(false)
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
		s.reader = nil
	}
}

// maintain keeps the command session connected and proves it is still alive.
//
// It is the only place that waits for C-Gate: it reconnects when the session
// is down, and once up sends a periodic noop to detect a silent drop before a
// real command runs into it.
func (s *commandSession) maintain() {
	// Polled on the short interval rather than slept through the heartbeat
	// one, so a session that drops between heartbeats is rebuilt promptly. On
	// the long interval the loop would not notice for up to commandHeartbeat,
	// leaving recovery to whichever request happened to come along next —
	// which on an idle console is no recovery at all.
	nextHeartbeat := time.Now().Add(commandHeartbeat)
	for {
		switch {
		case !commandUp.Load():
			s.mu.Lock()
			s.drop()
			err := s.connect()
			s.mu.Unlock()
			if err != nil {
				log.Printf("Command session: %v — retrying in %s", err, dialRetryInterval)
			} else {
				nextHeartbeat = time.Now().Add(commandHeartbeat)
			}

		case time.Now().After(nextHeartbeat):
			if _, err := s.send("noop"); err != nil {
				log.Printf("Command session: heartbeat failed: %v", err)
			} else {
				log.Printf("Command session: heartbeat ok")
			}
			nextHeartbeat = time.Now().Add(commandHeartbeat)
		}

		time.Sleep(dialRetryInterval)
	}
}

func (s *commandSession) send(cmd string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn == nil {
		// One attempt. maintain() is what waits out an outage; a request
		// arriving during one is answered with an error, not a hang.
		if err := s.connect(); err != nil {
			return nil, err
		}
	}

	log.Printf("Command session: sending %q", cmd)

	if _, err := fmt.Fprintf(s.conn, "%s\r\n", cmd); err != nil {
		// A failed write usually means C-Gate restarted under a session that
		// looked fine. One reconnect and resend covers that transparently; if
		// the reconnect fails too, report it rather than waiting.
		log.Printf("Command session: write failed: %v — reconnecting", err)
		s.drop()
		if err := s.connect(); err != nil {
			return nil, err
		}
		if _, err := fmt.Fprintf(s.conn, "%s\r\n", cmd); err != nil {
			log.Printf("Command session: write failed after reconnect: %v", err)
			s.drop()
			return nil, err
		}
	}

	// Read response lines
	var lines []string
	for {
		s.conn.SetReadDeadline(time.Now().Add(commandReadDeadline))
		line, err := s.reader.ReadString('\n')
		if err != nil {
			if len(lines) > 0 {
				break // got at least some response
			}
			// Deliberately not resent: the command may already have taken
			// effect at the far end, and repeating an ON is worse than
			// reporting the failure.
			log.Printf("Command session: read failed: %v — dropping the session", err)
			s.drop()
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		lines = append(lines, line)

		// Single-line response or last line of multi-line (no dash after code)
		if len(line) >= 3 && (len(line) == 3 || line[3] != '-') {
			break
		}
	}
	s.conn.SetReadDeadline(time.Time{})
	log.Printf("Command session: response %v", lines)
	return lines, nil
}

func handleCGate(w http.ResponseWriter, r *http.Request) {
	cmd := r.URL.Query().Get("cmd")
	if cmd == "" {
		http.Error(w, `{"error":"missing cmd parameter"}`, http.StatusBadRequest)
		return
	}

	lines, err := cmdSession.send(cmd)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
		return
	}

	// Broadcast command and response to WebSocket clients
	hub.broadcast(map[string]string{
		"stream": "command",
		"data":   "> " + cmd,
		"time":   time.Now().Format("15:04:05"),
	})
	for _, line := range lines {
		hub.broadcast(map[string]string{
			"stream": "response",
			"data":   line,
			"time":   time.Now().Format("15:04:05"),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"cmd":      cmd,
		"response": lines,
	})
}

func handleWS(ws *websocket.Conn) {
	hub.add(ws)
	defer hub.remove(ws)
	// Keep connection alive by reading (blocks until close)
	buf := make([]byte, 512)
	for {
		if _, err := ws.Read(buf); err != nil {
			break
		}
	}
}

// writeStatus renders the shared health/ready body.
func writeStatus(w http.ResponseWriter, code int) {
	event, status, command := eventStreamUp.Load(), statusStreamUp.Load(), commandUp.Load()
	state := "degraded"
	if event && status && command {
		state = "ok"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": state,
		"connections": map[string]bool{
			"command": command,
			"event":   event,
			"status":  status,
		},
	})
}

// handleHealth reports liveness: the bridge is up and serving. It returns 200
// even when C-Gate is unreachable, because callers use this to decide whether
// to restart the container and C-Gate needs up to a minute to sync its
// networks on a cold start — failing the probe during that window would turn
// a normal startup into a restart loop. The body carries the real detail.
// Gate on /ready instead if you need C-Gate itself to be up.
func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeStatus(w, http.StatusOK)
}

// handleReady reports readiness: every connection the bridge needs is
// established. Returns 503 until then, so clients can hold off their initial
// poll rather than retrying into "408 Operation failed" while C-Gate starts.
//
// This tracks the bridge's own TCP connections, not C-Gate's network state —
// a project can still be mid-sync when this first returns 200.
func handleReady(w http.ResponseWriter, r *http.Request) {
	code := http.StatusOK
	if !(eventStreamUp.Load() && statusStreamUp.Load() && commandUp.Load()) {
		code = http.StatusServiceUnavailable
	}
	writeStatus(w, code)
}

func main() {
	log.Printf("C-Gate Web Console starting on %s", listenAddr)

	// Start streaming from event and status ports
	go streamPort(cgateEventPort, "event", &eventStreamUp)
	go streamPort(cgateStatusPort, "status", &statusStreamUp)

	// Keep the command connection up. Backgrounded rather than blocking
	// startup: the console has to be reachable while C-Gate is down, which is
	// exactly when someone wants to look at it.
	go cmdSession.maintain()

	// Routes
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, _ := consoleHTML.ReadFile("console.html")
		w.Header().Set("Content-Type", "text/html")
		w.Write(data)
	})
	http.HandleFunc("/cgate", handleCGate)
	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/ready", handleReady)
	http.Handle("/ws", websocket.Handler(handleWS))

	log.Fatal(http.ListenAndServe(listenAddr, nil))
}
