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

	// Read deadline for stream connections — if nothing arrives within
	// this window we assume the connection is dead and reconnect.
	streamReadDeadline = 5 * time.Minute

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
// Reconnects automatically on any error or timeout.
func streamPort(port, streamName string) {
	for {
		conn := dialTCP(port)
		scanner := bufio.NewScanner(conn)
		alive := true
		for alive {
			// Set a read deadline so we detect dead connections even when
			// C-Gate is quiet (no events/status changes for a while).
			conn.SetReadDeadline(time.Now().Add(streamReadDeadline))
			if scanner.Scan() {
				line := scanner.Text()
				hub.broadcast(map[string]string{
					"stream": streamName,
					"data":   line,
					"time":   time.Now().Format("15:04:05"),
				})
			} else {
				alive = false
			}
		}
		if err := scanner.Err(); err != nil {
			log.Printf("Stream %s error: %v — reconnecting", streamName, err)
		} else {
			log.Printf("Stream %s disconnected (EOF) — reconnecting", streamName)
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
	log.Printf("Command session: ready")
}

func (s *commandSession) reconnect() {
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

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

func main() {
	log.Printf("C-Gate Web Console starting on %s", listenAddr)

	// Start streaming from event and status ports
	go streamPort(cgateEventPort, "event")
	go streamPort(cgateStatusPort, "status")

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
	http.Handle("/ws", websocket.Handler(handleWS))

	log.Fatal(http.ListenAndServe(listenAddr, nil))
}
