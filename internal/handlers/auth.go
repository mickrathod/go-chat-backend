package handlers

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type UserRecord struct {
	Username     string    `json:"username"`
	DisplayName  string    `json:"display_name"`
	PasswordHash string    `json:"-"`
	Avatar       string    `json:"avatar"`
	Bio          string    `json:"bio"`
	Status       string    `json:"status"`
	IsPro        bool      `json:"is_pro"`
	CreatedAt    time.Time `json:"created_at"`
}

type UserStore struct {
	mu    sync.RWMutex
	users map[string]*UserRecord
}

var globalUsers = &UserStore{
	users: make(map[string]*UserRecord),
}

var userDB *sql.DB

// SetUserDB initializes MySQL persistence for users and loads existing accounts
func SetUserDB(db *sql.DB) {
	userDB = db
	if db == nil {
		return
	}

	rows, err := db.Query("SELECT username, display_name, password_hash, avatar, bio, status, is_pro, created_at FROM users")
	if err != nil {
		log.Printf("⚠️  [DB] failed to query users from MySQL: %v", err)
		return
	}
	defer rows.Close()

	globalUsers.mu.Lock()
	count := 0
	for rows.Next() {
		var u UserRecord
		var isPro int
		if err := rows.Scan(&u.Username, &u.DisplayName, &u.PasswordHash, &u.Avatar, &u.Bio, &u.Status, &isPro, &u.CreatedAt); err == nil {
			u.IsPro = (isPro == 1)
			globalUsers.users[strings.ToLower(u.Username)] = &u
			count++
		}
	}
	globalUsers.mu.Unlock()
	log.Printf("👥 [DB] loaded %d users from MySQL", count)

	if count == 0 {
		globalUsers.mu.RLock()
		defer globalUsers.mu.RUnlock()
		for _, u := range globalUsers.users {
			isProInt := 0
			if u.IsPro {
				isProInt = 1
			}
			_, _ = db.Exec(
				"INSERT IGNORE INTO users (username, display_name, password_hash, avatar, bio, status, is_pro, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
				u.Username, u.DisplayName, u.PasswordHash, u.Avatar, u.Bio, u.Status, isProInt, u.CreatedAt,
			)
		}
		log.Println("👥 [DB] seeded default users into MySQL")
	}
}

func init() {
	// Pre-seed demo users with default password "password123"
	defaultUsers := []string{"Manav", "Sarah", "Alex", "David"}
	for _, name := range defaultUsers {
		u := strings.ToLower(name)
		globalUsers.users[u] = &UserRecord{
			Username:     name,
			DisplayName:  name,
			PasswordHash: hashPassword("password123", u),
			Avatar:       fmt.Sprintf("https://api.dicebear.com/7.x/bottts/svg?seed=%s", name),
			Bio:          fmt.Sprintf("Hey there! I am using GoChat."),
			Status:       "online",
			CreatedAt:    time.Now(),
		}
	}
}

func hashPassword(password string, salt string) string {
	hasher := sha256.New()
	hasher.Write([]byte(password + ":" + salt + ":gochat_secret_salt"))
	return hex.EncodeToString(hasher.Sum(nil))
}

type RegisterRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
	Avatar      string `json:"avatar"`
	Bio         string `json:"bio"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type AuthResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	User    *UserRecord `json:"user,omitempty"`
	Token   string      `json:"token,omitempty"`
}

// HandleRegister handles new user registration
func (h *Handler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(AuthResponse{Success: false, Message: "Invalid request payload"})
		return
	}

	username := strings.TrimSpace(req.Username)
	password := req.Password
	displayName := strings.TrimSpace(req.DisplayName)

	if len(username) < 3 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(AuthResponse{Success: false, Message: "Username must be at least 3 characters long"})
		return
	}
	if len(password) < 4 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(AuthResponse{Success: false, Message: "Password must be at least 4 characters long"})
		return
	}
	if displayName == "" {
		displayName = username
	}

	uKey := strings.ToLower(username)

	globalUsers.mu.Lock()
	defer globalUsers.mu.Unlock()

	if _, exists := globalUsers.users[uKey]; exists {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(AuthResponse{Success: false, Message: "Username is already registered. Please choose another or log in."})
		return
	}

	avatar := req.Avatar
	if avatar == "" {
		avatar = fmt.Sprintf("https://api.dicebear.com/7.x/bottts/svg?seed=%s", username)
	}

	bio := req.Bio
	if bio == "" {
		bio = "Hey there! I am using GoChat."
	}

	newUser := &UserRecord{
		Username:     username,
		DisplayName:  displayName,
		PasswordHash: hashPassword(password, uKey),
		Avatar:       avatar,
		Bio:          bio,
		Status:       "online",
		CreatedAt:    time.Now(),
	}

	globalUsers.users[uKey] = newUser

	if userDB != nil {
		isProInt := 0
		if newUser.IsPro {
			isProInt = 1
		}
		_, err := userDB.Exec(
			"INSERT INTO users (username, display_name, password_hash, avatar, bio, status, is_pro, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			newUser.Username, newUser.DisplayName, newUser.PasswordHash, newUser.Avatar, newUser.Bio, newUser.Status, isProInt, newUser.CreatedAt,
		)
		if err != nil {
			log.Printf("⚠️  [DB] failed to persist registered user %s to MySQL: %v", newUser.Username, err)
		} else {
			log.Printf("👤 [DB] persisted user @%s to MySQL", newUser.Username)
		}
	}

	token := fmt.Sprintf("token_%s_%d", uKey, time.Now().UnixNano())

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(AuthResponse{
		Success: true,
		Message: "Registration successful",
		User:    newUser,
		Token:   token,
	})
}

// HandleLogin authenticates an existing user
func (h *Handler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(AuthResponse{Success: false, Message: "Invalid request payload"})
		return
	}

	username := strings.TrimSpace(req.Username)
	password := req.Password
	uKey := strings.ToLower(username)

	globalUsers.mu.RLock()
	user, exists := globalUsers.users[uKey]
	globalUsers.mu.RUnlock()

	if !exists {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(AuthResponse{Success: false, Message: "Invalid username or password"})
		return
	}

	expectedHash := hashPassword(password, uKey)
	if user.PasswordHash != expectedHash {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(AuthResponse{Success: false, Message: "Invalid username or password"})
		return
	}

	token := fmt.Sprintf("token_%s_%d", uKey, time.Now().UnixNano())

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AuthResponse{
		Success: true,
		Message: "Login successful",
		User:    user,
		Token:   token,
	})
}

// HandleUpdateProfile allows updating display name, avatar, bio, or status
func (h *Handler) HandleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Avatar      string `json:"avatar"`
		Bio         string `json:"bio"`
		Status      string `json:"status"`
		IsPro       *bool  `json:"is_pro"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(AuthResponse{Success: false, Message: "Invalid payload"})
		return
	}

	uKey := strings.ToLower(strings.TrimSpace(req.Username))

	globalUsers.mu.Lock()
	user, exists := globalUsers.users[uKey]
	if !exists {
		globalUsers.mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(AuthResponse{Success: false, Message: "User not found"})
		return
	}

	if req.DisplayName != "" {
		user.DisplayName = strings.TrimSpace(req.DisplayName)
	}
	if req.Avatar != "" {
		user.Avatar = req.Avatar
	}
	if req.Bio != "" {
		user.Bio = strings.TrimSpace(req.Bio)
	}
	if req.Status != "" {
		user.Status = req.Status
	}
	if req.IsPro != nil {
		user.IsPro = *req.IsPro
	}

	if userDB != nil {
		isProInt := 0
		if user.IsPro {
			isProInt = 1
		}
		_, err := userDB.Exec(
			"UPDATE users SET display_name=?, avatar=?, bio=?, status=?, is_pro=? WHERE username=?",
			user.DisplayName, user.Avatar, user.Bio, user.Status, isProInt, user.Username,
		)
		if err != nil {
			log.Printf("⚠️  [DB] failed to update profile in MySQL for %s: %v", user.Username, err)
		} else {
			log.Printf("👤 [DB] updated profile in MySQL for @%s (is_pro=%v)", user.Username, user.IsPro)
		}
	}
	globalUsers.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AuthResponse{
		Success: true,
		Message: "Profile updated successfully",
		User:    user,
	})
}

// HandleGetProfile returns a user's public profile
func (h *Handler) HandleGetProfile(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.URL.Query().Get("username"))
	if username == "" {
		http.Error(w, "Missing username query param", http.StatusBadRequest)
		return
	}

	uKey := strings.ToLower(username)
	globalUsers.mu.RLock()
	user, exists := globalUsers.users[uKey]
	globalUsers.mu.RUnlock()

	if !exists {
		// If not registered, return dynamic profile
		user = &UserRecord{
			Username:    username,
			DisplayName: username,
			Avatar:      fmt.Sprintf("https://api.dicebear.com/7.x/bottts/svg?seed=%s", username),
			Bio:         "Hey there! I am using GoChat.",
			Status:      "offline",
			CreatedAt:   time.Now(),
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}
