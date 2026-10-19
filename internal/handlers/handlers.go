package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"go-chat-backend/internal/hub"
	"go-chat-backend/internal/limiter"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for dev / cross-origin requests
	},
}

// Handler bundles all HTTP and WebSocket handlers
type Handler struct {
	Hub *hub.Hub
}

// NewHandler creates a new Handler instance
func NewHandler(h *hub.Hub) *Handler {
	return &Handler{Hub: h}
}

// EnableCORS wraps an HTTP handler to inject standard CORS headers
func EnableCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next(w, r)
	}
}

func getClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		return xrip
	}
	return r.RemoteAddr
}

// HandleWS handles websocket upgrade and attaches client to the hub
func (h *Handler) HandleWS(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.URL.Query().Get("username"))
	if username == "" {
		username = fmt.Sprintf("User_%d", time.Now().Unix()%10000)
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("❌ WebSocket upgrade failed: %v", err)
		return
	}

	client := &hub.Client{
		Hub:         h.Hub,
		Conn:        conn,
		Send:        make(chan []byte, 256),
		Username:    username,
		IP:          getClientIP(r),
		RateLimiter: limiter.NewRateLimiter(25.0, 50.0),
	}

	h.Hub.Register <- client

	go client.WritePump()
	go client.ReadPump()
}

// HandleHistory returns recent chat history with optional filtering
func (h *Handler) HandleHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	channelFilter := strings.TrimSpace(r.URL.Query().Get("channel"))
	userFilter := strings.TrimSpace(r.URL.Query().Get("user"))

	history := h.Hub.GetHistory(channelFilter, userFilter)
	json.NewEncoder(w).Encode(history)
}

// HandleHealth provides a standard health check JSON endpoint
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	activeUsers, activeSockets, routedMsgs, uptime := h.Hub.GetStats()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":          "healthy",
		"service":         "gonext-chat-engine",
		"version":         "2.0.0",
		"uptime":          uptime.Round(time.Second).String(),
		"active_users":    activeUsers,
		"active_sockets":  activeSockets,
		"messages_routed": routedMsgs,
		"timestamp":       time.Now().Format(time.RFC3339),
	})
}

// HandleStats returns runtime memory and Goroutine telemetry
func (h *Handler) HandleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	activeUsers, activeSockets, routedMsgs, uptime := h.Hub.GetStats()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"uptime_seconds":  uptime.Seconds(),
		"active_users":    activeUsers,
		"active_sockets":  activeSockets,
		"messages_routed": routedMsgs,
		"goroutines":      runtime.NumGoroutine(),
		"memory_alloc_mb": fmt.Sprintf("%.2f MB", float64(m.Alloc)/1024/1024),
		"memory_sys_mb":   fmt.Sprintf("%.2f MB", float64(m.Sys)/1024/1024),
		"gc_cycles":       m.NumGC,
	})
}

// HandleUpload handles multipart file and image uploads
func (h *Handler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 25 MB max memory
	if err := r.ParseMultipartForm(25 << 20); err != nil {
		http.Error(w, "File too large (max 25MB)", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Invalid file upload", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Ensure uploads directory exists
	uploadDir := "./uploads"
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		http.Error(w, "Failed to create upload directory", http.StatusInternalServerError)
		return
	}

	// Safe unique filename
	ext := filepath.Ext(header.Filename)
	safeBase := strings.ReplaceAll(header.Filename, " ", "_")
	safeName := fmt.Sprintf("%d_%s", time.Now().UnixNano(), safeBase)
	destPath := filepath.Join(uploadDir, safeName)

	destFile, err := os.Create(destPath)
	if err != nil {
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}
	defer destFile.Close()

	written, err := io.Copy(destFile, file)
	if err != nil {
		http.Error(w, "Failed to write file contents", http.StatusInternalServerError)
		return
	}

	// Determine attachment type (image vs document)
	lowerExt := strings.ToLower(ext)
	fileType := "document"
	if lowerExt == ".jpg" || lowerExt == ".jpeg" || lowerExt == ".png" || lowerExt == ".gif" || lowerExt == ".webp" || lowerExt == ".svg" {
		fileType = "image"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"url":  fmt.Sprintf("/uploads/%s", safeName),
		"name": header.Filename,
		"type": fileType,
		"size": written,
	})
}
