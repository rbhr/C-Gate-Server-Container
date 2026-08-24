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

// dialTCP connects to a C-Gate port with retries and enables TCP keepalive
func dialTCP(port string) net.Conn {
	addr := net.JoinHostPort(cgateHost, port)
	for {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err == nil {
			// Enable TCP keepalive so the OS detects dead connections
			if tc, ok := conn.(*net.TCPConn); ok {
				tc.SetKeepAlive(true)
				tc.SetKeepAlivePeriod(keepAliveInterval)
			}
			log.Printf("Connected to C-Gate %s", addr)
			return conn
		}
		log.Printf("Waiting for C-Gate on %s: %v", addr, err)
		time.Sleep(3 * time.Second)
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
// dialTCP) stays as the backstop for the connection genuinely going away.
func streamPort(port, streamName string, up *atomic.Bool) {
	for {
		conn := dialTCP(port)
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

func (s *commandSession) connect() {
	log.Printf("Command session: connecting to C-Gate command port %s", cgateCommandPort)
	s.conn = dialTCP(cgateCommandPort)
	s.reader = bufio.NewReader(s.conn)
	// Drain the connect banner
	s.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			break
		}
		_ = line
	}
	s.conn.SetReadDeadline(time.Time{})
	commandUp.Store(true)
	log.Printf("Command session: ready")
}

func (s *commandSession) reconnect() {
	commandUp.Store(false)
	log.Printf("Command session: reconnecting")
	if s.conn != nil {
		s.conn.Close()
	}
	s.connect()
}

// heartbeat periodically sends a noop command to keep the command
// connection alive and detect silent drops before a real command fails.
func (s *commandSession) heartbeat() {
	for {
		time.Sleep(commandHeartbeat)
		_, err := s.send("noop")
		if err != nil {
			log.Printf("Command session: heartbeat failed: %v", err)
		} else {
			log.Printf("Command session: heartbeat ok")
		}
	}
}

func (s *commandSession) send(cmd string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn == nil {
		s.connect()
	}

	log.Printf("Command session: sending %q", cmd)

	_, err := fmt.Fprintf(s.conn, "%s\r\n", cmd)
	if err != nil {
		log.Printf("Command session: write failed: %v — reconnecting", err)
		s.reconnect()
		_, err = fmt.Fprintf(s.conn, "%s\r\n", cmd)
		if err != nil {
			log.Printf("Command session: write failed after reconnect: %v", err)
			return nil, err
		}
	}

	// Read response lines
	var lines []string
	for {
		s.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := s.reader.ReadString('\n')
		if err != nil {
			if len(lines) > 0 {
				break // got at least some response
			}
			log.Printf("Command session: read failed: %v — reconnecting", err)
			s.reconnect()
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

	// Initialize command connection and start heartbeat
	go func() {
		cmdSession.mu.Lock()
		cmdSession.connect()
		cmdSession.mu.Unlock()
		go cmdSession.heartbeat()
	}()

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
