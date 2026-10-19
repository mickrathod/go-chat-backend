package hub

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/gorilla/websocket"
	"go-chat-backend/internal/limiter"
	"go-chat-backend/internal/models"
)

// Client represents a single WebSocket connection
type Client struct {
	Hub         *Hub
	Conn        *websocket.Conn
	Send        chan []byte
	Username    string
	IP          string
	RateLimiter *limiter.RateLimiter
}

// ReadPump pumps messages from the websocket connection to the hub
func (c *Client) ReadPump() {
	defer func() {
		c.Hub.Unregister <- c
		c.Conn.Close()
	}()

	c.Conn.SetReadLimit(65536) // 64 KB max frame size
	c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, messageBytes, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("⚠️ WebSocket read error for %s: %v", c.Username, err)
			}
			break
		}

		// Anti-flood rate limiting
		if !c.RateLimiter.Allow() {
			warn := models.Message{
				ID:          fmt.Sprintf("warn_%d", time.Now().UnixNano()),
				Type:        "system",
				FromUser:    "System",
				Ciphertext:  "⚠️ Rate limit exceeded. Please slow down your messages.",
				IsEncrypted: false,
				Timestamp:   time.Now(),
			}
			warnData, _ := json.Marshal(warn)
			select {
			case c.Send <- warnData:
			default:
			}
			continue
		}

		var msg models.Message
		if err := json.Unmarshal(messageBytes, &msg); err == nil {
			if msg.ID == "" {
				msg.ID = fmt.Sprintf("msg_%d", time.Now().UnixNano())
			}
			msg.FromUser = c.Username
			if msg.Timestamp.IsZero() {
				msg.Timestamp = time.Now()
			}
			if msg.TTLSeconds > 0 && msg.ExpiresAt == 0 {
				msg.ExpiresAt = time.Now().Add(time.Duration(msg.TTLSeconds) * time.Second).UnixMilli()
			}

			data, err := json.Marshal(msg)
			if err == nil {
				c.Hub.Broadcast <- data
			}
		}
	}
}

// WritePump pumps messages from the hub to the websocket connection
func (c *Client) WritePump() {
	ticker := time.NewTicker(20 * time.Second)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.Conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}

			w, err := c.Conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Append queued chat messages to the current websocket frame
			n := len(c.Send)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				w.Write(<-c.Send)
			}

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
