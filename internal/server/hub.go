package server

import (
	"encoding/json"
	"sync"

	"github.com/gorilla/websocket"
)

type hubMessage struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

type hub struct {
	mu      sync.RWMutex
	clients map[*websocket.Conn]struct{}
}

func newHub() *hub {
	return &hub{clients: make(map[*websocket.Conn]struct{})}
}

func (h *hub) add(conn *websocket.Conn) {
	h.mu.Lock()
	h.clients[conn] = struct{}{}
	h.mu.Unlock()
}

func (h *hub) remove(conn *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, conn)
	h.mu.Unlock()
	_ = conn.Close()
}

func (h *hub) broadcast(eventType string, payload any) {
	data, err := json.Marshal(hubMessage{Type: eventType, Data: payload})
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for conn := range h.clients {
		_ = conn.SetWriteDeadline(now().Add(websocketWriteTimeout))
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			go h.remove(conn)
		}
	}
}
