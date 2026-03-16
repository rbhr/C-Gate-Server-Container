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
	cgateHost       = "localhost"
	cgateCommandPort = "20023"
	cgateEventPort   = "20024"
	cgateStatusPort  = "20025"
	listenAddr       = ":8980"
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
	delete(h.clients, ws)
	h.mu.Unlock()
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

// connectTCP dials a C-Gate port with retries
func connectTCP(port string) net.Conn {
	addr := net.JoinHostPort(cgateHost, port)
	for {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err == nil {
			log.Printf("Connected to C-Gate %s", addr)
			return conn
		}
		log.Printf("Waiting for C-Gate on %s: %v", addr, err)
		time.Sleep(3 * time.Second)
	}
}

// streamPort reads lines from a C-Gate port and broadcasts them
func streamPort(port, streamName string) {
	for {
		conn := connectTCP(port)
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			line := scanner.Text()
			hub.broadcast(map[string]string{
				"stream": streamName,
				"data":   line,
				"time":   time.Now().Format("15:04:05"),
			})
		}
		log.Printf("Disconnected from %s stream, reconnecting...", streamName)
		conn.Close()
		time.Sleep(2 * time.Second)
	}
}

// commandMu serialises command/response pairs on the control socket
var (
	commandConn net.Conn
	commandMu   sync.Mutex
)

func initCommandConn() {
	commandConn = connectTCP(cgateCommandPort)
	// Drain the connect banner
	commandConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	commandConn.Read(buf)
	commandConn.SetReadDeadline(time.Time{})
}

func sendCommand(cmd string) ([]string, error) {
	commandMu.Lock()
	defer commandMu.Unlock()

	if commandConn == nil {
		initCommandConn()
	}

	_, err := fmt.Fprintf(commandConn, "%s\r\n", cmd)
	if err != nil {
		// Reconnect and retry once
		commandConn.Close()
		initCommandConn()
		_, err = fmt.Fprintf(commandConn, "%s\r\n", cmd)
		if err != nil {
			return nil, err
		}
	}

	// Read response lines (C-Gate sends a response code like "200 ..." or "300-..." for multi-line)
	var lines []string
	reader := bufio.NewReader(commandConn)
	for {
		commandConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		lines = append(lines, line)

		// Single-line response or last line of multi-line (no dash after code)
		if len(line) >= 3 {
			code := line[:3]
			if len(line) == 3 || (len(line) > 3 && line[3] != '-') {
				_ = code
				break
			}
		}
	}
	commandConn.SetReadDeadline(time.Time{})
	return lines, nil
}

func handleCGate(w http.ResponseWriter, r *http.Request) {
	cmd := r.URL.Query().Get("cmd")
	if cmd == "" {
		http.Error(w, `{"error":"missing cmd parameter"}`, http.StatusBadRequest)
		return
	}

	lines, err := sendCommand(cmd)
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

func main() {
	log.Printf("C-Gate Web Console starting on %s", listenAddr)

	// Start streaming from event and status ports
	go streamPort(cgateEventPort, "event")
	go streamPort(cgateStatusPort, "status")

	// Initialize command connection
	go func() {
		commandMu.Lock()
		initCommandConn()
		commandMu.Unlock()
	}()

	// Routes
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, _ := consoleHTML.ReadFile("console.html")
		w.Header().Set("Content-Type", "text/html")
		w.Write(data)
	})
	http.HandleFunc("/cgate", handleCGate)
	http.Handle("/ws", websocket.Handler(handleWS))

	log.Fatal(http.ListenAndServe(listenAddr, nil))
}
