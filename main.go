package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go-chat-backend/internal/handlers"
	"go-chat-backend/internal/hub"
	"go-chat-backend/internal/store"
)

//go:embed all:dist
var distFS embed.FS

func main() {
	// 1. Root Context for Graceful Shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 2. Connect to MySQL (defaults to free TiDB Cloud serverless database)
	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		dsn = "3wGBcF94Sue8cXM.root:y77mPfjpBoF9ZGJD@tcp(gateway01.ap-northeast-1.prod.aws.tidbcloud.com:4000)/test?tls=true&charset=utf8mb4"
	}
	var msgStore hub.MessageStore
	db, err := store.Open(dsn)
	if err != nil {
		log.Printf("⚠️  [DB] MySQL unavailable, history will NOT survive a restart: %v", err)
	} else {
		defer db.Close()
		msgStore = db
		log.Println("🗄️  [DB] connected to MySQL")
		handlers.SetUserDB(db.DB())
		handlers.SetChannelDB(db.DB())
	}

	// 3. Initialize Core Hub
	chatHub := hub.NewHub(msgStore)
	go chatHub.Run(ctx)

	// 3. Initialize HTTP & WebSocket Handlers
	h := handlers.NewHandler(chatHub)
	mux := http.NewServeMux()

	mux.HandleFunc("/ws", h.HandleWS)
	mux.HandleFunc("/api/history", handlers.EnableCORS(h.HandleHistory))
	mux.HandleFunc("/api/health", handlers.EnableCORS(h.HandleHealth))
	mux.HandleFunc("/api/stats", handlers.EnableCORS(h.HandleStats))
	mux.HandleFunc("/api/upload", handlers.EnableCORS(h.HandleUpload))
	mux.HandleFunc("/api/auth/register", handlers.EnableCORS(h.HandleRegister))
	mux.HandleFunc("/api/auth/login", handlers.EnableCORS(h.HandleLogin))
	mux.HandleFunc("/api/users/profile", handlers.EnableCORS(h.HandleUpdateProfile))
	mux.HandleFunc("/api/users/get", handlers.EnableCORS(h.HandleGetProfile))
	mux.HandleFunc("/api/channels", handlers.EnableCORS(h.HandleGetChannels))
	mux.HandleFunc("/api/channels/create", handlers.EnableCORS(h.HandleCreateChannel))
	mux.HandleFunc("/api/channels/invite", handlers.EnableCORS(h.HandleInviteMembers))
	mux.HandleFunc("/api/channels/members", handlers.EnableCORS(h.HandleGetChannelMembers))

	// Serve Uploaded Files statically
	_ = os.MkdirAll("./uploads", 0755)
	uploadsFS := http.FileServer(http.Dir("./uploads"))
	mux.Handle("/uploads/", http.StripPrefix("/uploads/", uploadsFS))

	// Serve Static Merchant Admin SDK files
	sdkFileServer := http.FileServer(http.Dir("./sdk"))
	mux.Handle("/sdk/", http.StripPrefix("/sdk/", sdkFileServer))

	// Embedded Next.js Fullstack UI (SPA Routing Support)
	distSubFS, err := fs.Sub(distFS, "dist")
	if err != nil {
		log.Printf("⚠️  Embedded frontend not loaded: %v", err)
	} else {
		staticServer := http.FileServer(http.FS(distSubFS))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			cleanPath := strings.TrimPrefix(r.URL.Path, "/")
			if cleanPath == "" {
				staticServer.ServeHTTP(w, r)
				return
			}
			// If file exists in embedded dist (e.g. _next/static/..., favicon.ico), serve it
			f, err := distSubFS.Open(cleanPath)
			if err == nil {
				_ = f.Close()
				staticServer.ServeHTTP(w, r)
				return
			}
			// Otherwise fallback to index.html for SPA client-side routing
			r.URL.Path = "/"
			staticServer.ServeHTTP(w, r)
		})
	}

	// 4. Configure Server
	port := ":8080"
	server := &http.Server{
		Addr:         port,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// 5. Intercept OS Termination Signals (SIGINT, SIGTERM)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// 6. Launch Server Goroutine
	go func() {
		fmt.Println("==========================================================")
		fmt.Println("⚡ GONEXT MODULAR WEBSOCKET ENGINE (v2.0.0)")
		fmt.Println("==========================================================")
		fmt.Printf("💻 Fullstack Web Chat:  http://localhost%s/\n", port)
		fmt.Printf("🚀 WebSocket Gateway:   ws://localhost%s/ws\n", port)
		fmt.Printf("📁 Upload API:          http://localhost%s/api/upload\n", port)
		fmt.Printf("🌐 Health Check API:   http://localhost%s/api/health\n", port)
		fmt.Printf("📊 Real-Time Metrics:  http://localhost%s/api/stats\n", port)
		fmt.Printf("📜 Message History:    http://localhost%s/api/history\n", port)
		fmt.Printf("💼 Merchant Admin SDK: http://localhost%s/sdk/admin.html\n", port)
		fmt.Println("----------------------------------------------------------")
		fmt.Println("📎 File & Image Attachments Enabled")
		fmt.Println("==========================================================")

		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌ Server failed: %v", err)
		}
	}()

	// 7. Wait for Shutdown Signal
	<-stop
	fmt.Println("\n⚠️  Termination signal received. Initiating graceful shutdown...")

	cancel() // Closes hub and client channels

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("❌ Server forced to shutdown: %v", err)
	} else {
		fmt.Println("✅ Server exited cleanly. All connections closed safely.")
	}
}
