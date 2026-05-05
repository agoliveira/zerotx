package api

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// upgrader configures the WebSocket handshake. CheckOrigin is permissive
// because CORS is the access-control mechanism used elsewhere in the API.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// upgradeWS performs the WebSocket handshake and returns the connection,
// or writes a 400 response and returns an error.
func upgradeWS(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	return upgrader.Upgrade(w, r, nil)
}

// client is a single WS connection with its own send buffer. The hub
// pushes payloads into Send; the client's writer goroutine drains them
// to the socket. A full Send buffer means the client can't keep up; we
// drop the message and log once.
type client struct {
	conn   *websocket.Conn
	send   chan []byte
	hub    *hub
	closed chan struct{}
}

// hub is the WS broadcast manager. Add/remove clients via channels so
// concurrent ops on the client set don't need locks.
type hub struct {
	mu      sync.RWMutex
	clients map[*client]struct{}
}

func newHub() *hub {
	return &hub{
		clients: make(map[*client]struct{}),
	}
}

// ClientCount returns the number of currently connected WS clients.
// Used by the metrics endpoint.
func (h *hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// addClient registers a new WS connection with the hub and starts the
// per-client writer/reader goroutines.
func (h *hub) addClient(conn *websocket.Conn) {
	c := &client{
		conn:   conn,
		send:   make(chan []byte, 4), // small buffer; drop fast clients
		hub:    h,
		closed: make(chan struct{}),
	}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	count := len(h.clients)
	h.mu.Unlock()
	log.Printf("api: ws client connected (%d total)", count)
	go c.writePump()
	go c.readPump()
}

// removeClient deregisters a client and cleans up its resources. Safe to
// call multiple times for the same client.
func (h *hub) removeClient(c *client) {
	h.mu.Lock()
	if _, ok := h.clients[c]; !ok {
		h.mu.Unlock()
		return
	}
	delete(h.clients, c)
	count := len(h.clients)
	h.mu.Unlock()
	close(c.closed)
	_ = c.conn.Close()
	log.Printf("api: ws client disconnected (%d remaining)", count)
}

// broadcast sends payload to every connected client. Slow clients (full
// send buffer) drop this message; persistent backpressure logs a warning
// but doesn't block the broadcaster.
func (h *hub) broadcast(payload []byte) {
	h.mu.RLock()
	clients := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		select {
		case c.send <- payload:
		default:
			// Buffer full: drop. The client will see stale data; we'd
			// rather skip than block the entire daemon tick.
		}
	}
}

// clientCount is used by the broadcast loop to skip work when nobody's
// listening.
func (h *hub) clientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// closeAll forces all clients to disconnect. Called during shutdown.
func (h *hub) closeAll() {
	h.mu.Lock()
	clients := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	for _, c := range clients {
		h.removeClient(c)
	}
}

// writePump pushes messages from c.send to the socket. Uses a write
// deadline so a stuck client doesn't hang the goroutine forever.
func (c *client) writePump() {
	pingTicker := time.NewTicker(30 * time.Second)
	defer func() {
		pingTicker.Stop()
		c.hub.removeClient(c)
	}()
	for {
		select {
		case <-c.closed:
			return
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-pingTicker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// readPump drains incoming messages so the WebSocket library handles
// pings/pongs and close frames properly. We don't accept any client
// commands yet, so all received messages are discarded.
func (c *client) readPump() {
	defer c.hub.removeClient(c)
	c.conn.SetReadLimit(512)
	_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}
