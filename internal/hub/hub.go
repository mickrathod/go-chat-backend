package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"go-chat-backend/internal/models"
)

// MessageStore persists chat messages (implemented by store.MySQL)
type MessageStore interface {
	Save(msg models.Message)
	Recent(limit int) ([]models.Message, error)
}

// Hub maintains active clients, broadcast channels, and an in-memory cache of recent history
type Hub struct {
	clients     map[*Client]bool
	userClients map[string]map[*Client]bool

	Broadcast  chan []byte
	Register   chan *Client
	Unregister chan *Client

	history    []models.Message
	maxHistory int
	store      MessageStore

	startTime       time.Time
	totalRoutedMsgs uint64

	mu sync.RWMutex
}

// NewHub initializes and returns a new Hub instance.
// store may be nil, in which case history is kept in memory only.
func NewHub(store MessageStore) *Hub {
	h := &Hub{
		clients:         make(map[*Client]bool),
		userClients:     make(map[string]map[*Client]bool),
		Broadcast:       make(chan []byte, 2048),
		Register:        make(chan *Client),
		Unregister:      make(chan *Client),
		history:         make([]models.Message, 0, 500),
		maxHistory:      250,
		store:           store,
		startTime:       time.Now(),
		totalRoutedMsgs: 0,
	}

	if store != nil {
		recent, err := store.Recent(h.maxHistory)
		if err != nil {
			log.Printf("⚠️  [DB] could not load message history: %v", err)
		} else {
			h.history = append(h.history, recent...)
			log.Printf("📜 [DB] loaded %d messages from MySQL", len(recent))
		}
	}
	return h
}

// Run executes the main event loop for the Hub
func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			log.Println("🛑 Hub shutting down, closing all active client connections...")
			h.mu.Lock()
			for client := range h.clients {
				close(client.Send)
				delete(h.clients, client)
			}
			h.userClients = make(map[string]map[*Client]bool)
			h.mu.Unlock()
			return

		case client := <-h.Register:
			h.mu.Lock()
			h.clients[client] = true
			if _, exists := h.userClients[client.Username]; !exists {
				h.userClients[client.Username] = make(map[*Client]bool)
			}
			h.userClients[client.Username][client] = true
			totalUsers := len(h.userClients)
			totalConns := len(h.clients)
			h.mu.Unlock()

			log.Printf("🟢 [CONNECT] %s connected from %s (Sessions: %d, Total Users: %d, Total Sockets: %d)",
				client.Username, client.IP, len(h.userClients[client.Username]), totalUsers, totalConns)

			h.BroadcastUsersList()
			h.SendSystemNotice(fmt.Sprintf("%s joined the chat network", client.Username))

		case client := <-h.Unregister:
			h.mu.Lock()
			userLeftCompletely := false
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.Send)

				if sessions, found := h.userClients[client.Username]; found {
					delete(sessions, client)
					if len(sessions) == 0 {
						delete(h.userClients, client.Username)
						userLeftCompletely = true
					}
				}
			}
			totalUsers := len(h.userClients)
			totalConns := len(h.clients)
			h.mu.Unlock()

			log.Printf("🔴 [DISCONNECT] %s disconnected (Active Users: %d, Active Sockets: %d)",
				client.Username, totalUsers, totalConns)

			if userLeftCompletely {
				h.BroadcastUsersList()
				h.SendSystemNotice(fmt.Sprintf("%s went offline", client.Username))
			}

		case messageBytes := <-h.Broadcast:
			var msg models.Message
			if err := json.Unmarshal(messageBytes, &msg); err != nil {
				continue
			}

			atomic.AddUint64(&h.totalRoutedMsgs, 1)

			// Skip saving transient typing indicators, reactions, and webrtc call signaling into history
			if msg.Type != "typing" && msg.Type != "webrtc" && msg.Type != "reaction" {
				// Clients don't send ids; assign one so the message can be stored and keyed
				if msg.ID == "" || msg.Timestamp.IsZero() {
					if msg.ID == "" {
						msg.ID = fmt.Sprintf("msg_%d", time.Now().UnixNano())
					}
					if msg.Timestamp.IsZero() {
						msg.Timestamp = time.Now()
					}
					if data, err := json.Marshal(msg); err == nil {
						messageBytes = data
					}
				}

				h.mu.Lock()
				if len(h.history) >= h.maxHistory {
					h.history = h.history[1:]
				}
				h.history = append(h.history, msg)
				h.mu.Unlock()

				// Ephemeral messages are not persisted to database for privacy
				if h.store != nil && (msg.Type == "group" || msg.Type == "direct") && msg.TTLSeconds == 0 {
					h.store.Save(msg)
				}
			}

			// Route message: DIRECT (1-on-1 Private) vs GROUP / BROADCAST
			if (msg.Type == "direct" || msg.Type == "webrtc" || msg.Type == "reaction") && msg.ToUser != "" {
				h.mu.RLock()
				if targetSessions, ok := h.userClients[msg.ToUser]; ok {
					for targetClient := range targetSessions {
						select {
						case targetClient.Send <- messageBytes:
						default:
						}
					}
				}
				if senderSessions, ok := h.userClients[msg.FromUser]; ok {
					for senderClient := range senderSessions {
						select {
						case senderClient.Send <- messageBytes:
						default:
						}
					}
				}
				h.mu.RUnlock()
			} else {
				h.mu.RLock()
				for client := range h.clients {
					select {
					case client.Send <- messageBytes:
					default:
						close(client.Send)
						delete(h.clients, client)
						if sessions, ok := h.userClients[client.Username]; ok {
							delete(sessions, client)
							if len(sessions) == 0 {
								delete(h.userClients, client.Username)
							}
						}
					}
				}
				h.mu.RUnlock()
			}
		}
	}
}

// BroadcastUsersList sends the current list of online users to all clients
func (h *Hub) BroadcastUsersList() {
	h.mu.RLock()
	users := make([]models.User, 0, len(h.userClients))
	for username := range h.userClients {
		users = append(users, models.User{
			Username:  username,
			Avatar:    fmt.Sprintf("https://api.dicebear.com/7.x/bottts/svg?seed=%s", username),
			Connected: time.Now(),
		})
	}
	h.mu.RUnlock()

	payload, err := json.Marshal(map[string]interface{}{
		"type":  "user_list",
		"users": users,
	})
	if err != nil {
		return
	}

	h.mu.RLock()
	for client := range h.clients {
		select {
		case client.Send <- payload:
		default:
		}
	}
	h.mu.RUnlock()
}

// SendSystemNotice dispatches an automated system message to the broadcast channel
func (h *Hub) SendSystemNotice(text string) {
	msg := models.Message{
		ID:          fmt.Sprintf("sys_%d", time.Now().UnixNano()),
		Type:        "system",
		Channel:     "general",
		FromUser:    "System",
		Ciphertext:  text,
		IsEncrypted: false,
		Timestamp:   time.Now(),
	}
	data, err := json.Marshal(msg)
	if err == nil {
		h.Broadcast <- data
	}
}

// GetHistory returns a copy of message history with optional filters
func (h *Hub) GetHistory(channelFilter, userFilter string) []models.Message {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if channelFilter == "" && userFilter == "" {
		res := make([]models.Message, len(h.history))
		copy(res, h.history)
		return res
	}

	filtered := make([]models.Message, 0, len(h.history))
	for _, m := range h.history {
		if channelFilter != "" && m.Type == "group" && m.Channel == channelFilter {
			filtered = append(filtered, m)
		} else if userFilter != "" && m.Type == "direct" && (m.FromUser == userFilter || m.ToUser == userFilter) {
			filtered = append(filtered, m)
		} else if m.Type == "system" {
			filtered = append(filtered, m)
		}
	}
	return filtered
}

// GetStats returns active user, socket, message count, and uptime
func (h *Hub) GetStats() (int, int, uint64, time.Duration) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.userClients), len(h.clients), atomic.LoadUint64(&h.totalRoutedMsgs), time.Since(h.startTime)
}
