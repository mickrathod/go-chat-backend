package models

import "time"

// User represents an active user in the chat network
type User struct {
	Username  string    `json:"username"`
	Avatar    string    `json:"avatar"`
	Connected time.Time `json:"connected"`
}

// Message represents an event, direct message, channel broadcast, or file attachment
type Message struct {
	ID             string    `json:"id"`
	Type           string    `json:"type"` // "group", "direct", "system", "typing"
	Channel        string    `json:"channel,omitempty"`
	FromUser       string    `json:"from_user"`
	ToUser         string    `json:"to_user,omitempty"`
	Ciphertext     string    `json:"ciphertext"` // Message text or encrypted payload
	IV             string    `json:"iv,omitempty"`          // Initialization vector for AES-GCM
	IsEncrypted    bool      `json:"is_encrypted"`
	IsTyping       bool      `json:"is_typing,omitempty"`
	AttachmentURL  string    `json:"attachment_url,omitempty"`
	AttachmentName string    `json:"attachment_name,omitempty"`
	AttachmentType string    `json:"attachment_type,omitempty"` // "image", "document", etc.
	AttachmentSize int64     `json:"attachment_size,omitempty"`
	IsPro          bool      `json:"is_pro,omitempty"`
	TTLSeconds     int       `json:"ttl_seconds,omitempty"`
	ExpiresAt      int64     `json:"expires_at,omitempty"`
	Timestamp      time.Time `json:"timestamp"`
}
