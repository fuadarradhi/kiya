package web

import (
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type WebSocketConn struct {
	*websocket.Conn
	mu sync.Mutex
}

func NewWebSocket(w http.ResponseWriter, r *http.Request) (*WebSocketConn, error) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, err
	}
	return &WebSocketConn{Conn: conn}, nil
}

func (c *WebSocketConn) WriteJSON(v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.WriteJSON(v)
}

func (c *WebSocketConn) WriteMessage(messageType int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.WriteMessage(messageType, data)
}

func (c *WebSocketConn) ReadJSON(v interface{}) error {
	return c.Conn.ReadJSON(v)
}

func (c *WebSocketConn) ReadMessage() (int, []byte, error) {
	return c.Conn.ReadMessage()
}

func (c *WebSocketConn) Close() error {
	return c.Conn.Close()
}
