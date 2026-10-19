package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type ChannelRecord struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Icon        string    `json:"icon"`
	Description string    `json:"desc"`
	CreatedBy   string    `json:"created_by"`
	IsPrivate   bool      `json:"is_private"`
	CreatedAt   time.Time `json:"created_at"`
	MemberCount int       `json:"member_count"`
	Members     []string  `json:"members,omitempty"`
}

type ChannelStore struct {
	mu       sync.RWMutex
	channels map[string]*ChannelRecord
	members  map[string]map[string]string // channelID -> username -> role
}

var globalChannels = &ChannelStore{
	channels: make(map[string]*ChannelRecord),
	members:  make(map[string]map[string]string),
}

var channelDB *sql.DB

var channelSlugRegexp = regexp.MustCompile(`[^a-z0-9_-]+`)

func slugifyChannel(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	slug = channelSlugRegexp.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = fmt.Sprintf("ch-%d", time.Now().Unix()%10000)
	}
	return slug
}

// SetChannelDB initializes MySQL persistence for channels and loads existing ones
func SetChannelDB(db *sql.DB) {
	channelDB = db
	if db == nil {
		return
	}

	// 1. Ensure default 'general' channel exists
	var count int
	_ = db.QueryRow("SELECT COUNT(*) FROM channels").Scan(&count)
	if count == 0 {
		now := time.Now()
		_, _ = db.Exec(
			"INSERT INTO channels (id, name, icon, description, created_by, is_private, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
			"general", "General Discussion", "💬", "Welcome to GoChat! General discussions for all members.", "system", 0, now,
		)
		_, _ = db.Exec(
			"INSERT IGNORE INTO channel_members (channel_id, username, role, joined_at) VALUES (?, ?, ?, ?)",
			"general", "system", "owner", now,
		)
		log.Println("📢 [DB] seeded default 'General Discussion' channel into MySQL")
	}

	// 2. Load channels from MySQL
	rows, err := db.Query("SELECT id, name, icon, description, created_by, is_private, created_at FROM channels ORDER BY created_at ASC")
	if err != nil {
		log.Printf("⚠️  [DB] failed to query channels: %v", err)
		return
	}
	defer rows.Close()

	globalChannels.mu.Lock()
	defer globalChannels.mu.Unlock()

	for rows.Next() {
		var ch ChannelRecord
		var isPriv int
		if err := rows.Scan(&ch.ID, &ch.Name, &ch.Icon, &ch.Description, &ch.CreatedBy, &isPriv, &ch.CreatedAt); err == nil {
			ch.IsPrivate = (isPriv == 1)
			globalChannels.channels[ch.ID] = &ch
			if globalChannels.members[ch.ID] == nil {
				globalChannels.members[ch.ID] = make(map[string]string)
			}
		}
	}

	// 3. Load members
	mRows, err := db.Query("SELECT channel_id, username, role FROM channel_members")
	if err == nil {
		defer mRows.Close()
		for mRows.Next() {
			var chID, username, role string
			if err := mRows.Scan(&chID, &username, &role); err == nil {
				if globalChannels.members[chID] == nil {
					globalChannels.members[chID] = make(map[string]string)
				}
				globalChannels.members[chID][username] = role
			}
		}
	}

	log.Printf("📢 [DB] loaded %d custom channels from MySQL", len(globalChannels.channels))
}

// HandleGetChannels returns all channels visible to the requesting user
func (h *Handler) HandleGetChannels(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.URL.Query().Get("username"))

	globalChannels.mu.RLock()
	defer globalChannels.mu.RUnlock()

	result := make([]ChannelRecord, 0, len(globalChannels.channels))
	for _, ch := range globalChannels.channels {
		mMap := globalChannels.members[ch.ID]
		isMember := false
		if username != "" && mMap != nil {
			_, isMember = mMap[username]
		}

		// Show public channels OR private channels where user is a member
		if !ch.IsPrivate || isMember || ch.CreatedBy == username || ch.CreatedBy == "system" {
			chCopy := *ch
			chCopy.MemberCount = len(mMap)
			var membersList []string
			for m := range mMap {
				membersList = append(membersList, m)
			}
			chCopy.Members = membersList
			result = append(result, chCopy)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// HandleCreateChannel creates a new dynamic channel
func (h *Handler) HandleCreateChannel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Name        string   `json:"name"`
		Icon        string   `json:"icon"`
		Description string   `json:"desc"`
		CreatedBy   string   `json:"created_by"`
		IsPrivate   bool     `json:"is_private"`
		Members     []string `json:"members"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "Invalid payload"})
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "Channel name is required"})
		return
	}

	channelID := slugifyChannel(name)
	icon := req.Icon
	if icon == "" {
		icon = "💬"
	}

	desc := strings.TrimSpace(req.Description)
	createdBy := strings.TrimSpace(req.CreatedBy)
	if createdBy == "" {
		createdBy = "Anonymous"
	}

	now := time.Now()

	globalChannels.mu.Lock()
	// If id collision, append timestamp
	if _, exists := globalChannels.channels[channelID]; exists {
		channelID = fmt.Sprintf("%s-%d", channelID, now.Unix()%1000)
	}

	chRecord := &ChannelRecord{
		ID:          channelID,
		Name:        name,
		Icon:        icon,
		Description: desc,
		CreatedBy:   createdBy,
		IsPrivate:   req.IsPrivate,
		CreatedAt:   now,
	}
	globalChannels.channels[channelID] = chRecord
	globalChannels.members[channelID] = make(map[string]string)
	globalChannels.members[channelID][createdBy] = "owner"

	for _, m := range req.Members {
		m = strings.TrimSpace(m)
		if m != "" {
			globalChannels.members[channelID][m] = "member"
		}
	}
	globalChannels.mu.Unlock()

	// Persist to MySQL
	if channelDB != nil {
		isPrivInt := 0
		if req.IsPrivate {
			isPrivInt = 1
		}
		_, err := channelDB.Exec(
			"INSERT INTO channels (id, name, icon, description, created_by, is_private, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
			channelID, name, icon, desc, createdBy, isPrivInt, now,
		)
		if err != nil {
			log.Printf("⚠️  [DB] failed to persist channel %s to MySQL: %v", channelID, err)
		} else {
			log.Printf("📢 [DB] persisted channel #%s (%s) to MySQL", channelID, name)
		}

		// Persist owner and members
		_, _ = channelDB.Exec(
			"INSERT IGNORE INTO channel_members (channel_id, username, role, joined_at) VALUES (?, ?, ?, ?)",
			channelID, createdBy, "owner", now,
		)
		for _, m := range req.Members {
			m = strings.TrimSpace(m)
			if m != "" && m != createdBy {
				_, _ = channelDB.Exec(
					"INSERT IGNORE INTO channel_members (channel_id, username, role, joined_at) VALUES (?, ?, ?, ?)",
					channelID, m, "member", now,
				)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Channel created successfully",
		"channel": chRecord,
	})
}

// HandleInviteMembers adds members to an existing channel
func (h *Handler) HandleInviteMembers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ChannelID string   `json:"channel_id"`
		InvitedBy string   `json:"invited_by"`
		Members   []string `json:"members"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "Invalid payload"})
		return
	}

	chID := strings.TrimSpace(req.ChannelID)
	if chID == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "channel_id is required"})
		return
	}

	globalChannels.mu.Lock()
	ch, exists := globalChannels.channels[chID]
	if !exists {
		globalChannels.mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "Channel not found"})
		return
	}

	if globalChannels.members[chID] == nil {
		globalChannels.members[chID] = make(map[string]string)
	}

	now := time.Now()
	added := make([]string, 0)
	for _, m := range req.Members {
		m = strings.TrimSpace(m)
		if m != "" {
			if _, alreadyMember := globalChannels.members[chID][m]; !alreadyMember {
				globalChannels.members[chID][m] = "member"
				added = append(added, m)
			}
		}
	}
	globalChannels.mu.Unlock()

	// Persist to MySQL
	if channelDB != nil {
		for _, m := range added {
			_, _ = channelDB.Exec(
				"INSERT IGNORE INTO channel_members (channel_id, username, role, joined_at) VALUES (?, ?, ?, ?)",
				chID, m, "member", now,
			)
		}
		log.Printf("📢 [DB] invited %d users to channel #%s (%s)", len(added), chID, ch.Name)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Added %d members to %s", len(added), ch.Name),
		"added":   added,
	})
}

// HandleGetChannelMembers returns members of a channel
func (h *Handler) HandleGetChannelMembers(w http.ResponseWriter, r *http.Request) {
	chID := strings.TrimSpace(r.URL.Query().Get("channel_id"))
	if chID == "" {
		http.Error(w, "Missing channel_id", http.StatusBadRequest)
		return
	}

	globalChannels.mu.RLock()
	mMap := globalChannels.members[chID]
	var members []map[string]string
	for user, role := range mMap {
		members = append(members, map[string]string{
			"username": user,
			"role":     role,
		})
	}
	globalChannels.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(members)
}
