package store

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"regexp"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	"go-chat-backend/internal/models"
)

var migrations = []struct {
	name string
	sql  string
}{
	{
		name: "create messages table",
		sql: `CREATE TABLE IF NOT EXISTS messages (
			id              BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
			msg_id          VARCHAR(64)  NOT NULL,
			type            VARCHAR(16)  NOT NULL,
			channel         VARCHAR(100) NOT NULL DEFAULT '',
			from_user       VARCHAR(100) NOT NULL,
			to_user         VARCHAR(100) NOT NULL DEFAULT '',
			body            TEXT         NOT NULL,
			attachment_url  VARCHAR(512) NOT NULL DEFAULT '',
			attachment_name VARCHAR(255) NOT NULL DEFAULT '',
			attachment_type VARCHAR(32)  NOT NULL DEFAULT '',
			attachment_size BIGINT       NOT NULL DEFAULT 0,
			created_at      DATETIME(3)  NOT NULL,
			UNIQUE KEY uq_msg_id (msg_id),
			KEY idx_channel (type, channel, created_at),
			KEY idx_direct (type, from_user, to_user, created_at)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	},
	{
		name: "create users table",
		sql: `CREATE TABLE IF NOT EXISTS users (
			username        VARCHAR(100) PRIMARY KEY,
			display_name    VARCHAR(100) NOT NULL,
			password_hash   VARCHAR(255) NOT NULL,
			avatar          VARCHAR(512) NOT NULL DEFAULT '',
			bio             VARCHAR(512) NOT NULL DEFAULT '',
			status          VARCHAR(50)  NOT NULL DEFAULT 'offline',
			is_pro          TINYINT(1)   NOT NULL DEFAULT 0,
			created_at      DATETIME(3)  NOT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	},
	{
		name: "create channels table",
		sql: `CREATE TABLE IF NOT EXISTS channels (
			id          VARCHAR(100) PRIMARY KEY,
			name        VARCHAR(100) NOT NULL,
			icon        VARCHAR(32)  NOT NULL DEFAULT '💬',
			description VARCHAR(255) NOT NULL DEFAULT '',
			created_by  VARCHAR(100) NOT NULL,
			is_private  TINYINT(1)   NOT NULL DEFAULT 0,
			created_at  DATETIME(3)  NOT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	},
	{
		name: "create channel_members table",
		sql: `CREATE TABLE IF NOT EXISTS channel_members (
			channel_id  VARCHAR(100) NOT NULL,
			username    VARCHAR(100) NOT NULL,
			role        VARCHAR(32)  NOT NULL DEFAULT 'member',
			joined_at   DATETIME(3)  NOT NULL,
			PRIMARY KEY (channel_id, username),
			KEY idx_user_channels (username)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	},
}

var validDBName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// MySQL persists chat messages and user accounts. Writes are queued and applied by a single
// background goroutine so a slow database never blocks message routing.
type MySQL struct {
	db     *sql.DB
	queue  chan models.Message
	done   chan struct{}
	mu     sync.RWMutex
	closed bool
}

// DB returns the underlying *sql.DB connection pool
func (s *MySQL) DB() *sql.DB {
	return s.db
}

// Open connects to MySQL, creates the database and tables if missing, and
// starts the background writer.
func Open(dsn string) (*MySQL, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid MYSQL_DSN: %w", err)
	}
	if !validDBName.MatchString(cfg.DBName) {
		return nil, fmt.Errorf("MYSQL_DSN must name a database (letters, digits, underscore), got %q", cfg.DBName)
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC

	if err := createDatabase(cfg); err != nil {
		log.Printf("ℹ️  [DB] createDatabase: %v (continuing to connect directly)", err)
	}

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetConnMaxLifetime(3 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, m := range migrations {
		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			if myErr, ok := err.(*mysql.MySQLError); !ok || myErr.Number != 1050 {
				db.Close()
				return nil, fmt.Errorf("migration %s failed: %w", m.name, err)
			}
		}
		log.Printf("✅ [MIGRATION] %s verified", m.name)
	}

	s := &MySQL{
		db:    db,
		queue: make(chan models.Message, 1024),
		done:  make(chan struct{}),
	}
	go s.writer()
	return s, nil
}

func createDatabase(cfg *mysql.Config) error {
	server := cfg.Clone()
	server.DBName = ""
	db, err := sql.Open("mysql", server.FormatDSN())
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = db.ExecContext(ctx, fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", cfg.DBName))
	if err != nil {
		return fmt.Errorf("connect to MySQL at %s: %w", cfg.Addr, err)
	}
	return nil
}

// Save queues a message for insertion. It never blocks.
func (s *MySQL) Save(msg models.Message) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	select {
	case s.queue <- msg:
	default:
		log.Printf("⚠️  [DB] write queue full, message %s not saved", msg.ID)
	}
}

func (s *MySQL) writer() {
	defer close(s.done)
	for msg := range s.queue {
		_, err := s.db.Exec(
			`INSERT IGNORE INTO messages
				(msg_id, type, channel, from_user, to_user, body,
				 attachment_url, attachment_name, attachment_type, attachment_size, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			msg.ID, msg.Type, msg.Channel, msg.FromUser, msg.ToUser, msg.Ciphertext,
			msg.AttachmentURL, msg.AttachmentName, msg.AttachmentType, msg.AttachmentSize, msg.Timestamp.UTC(),
		)
		if err != nil {
			log.Printf("❌ [DB] failed to save message %s: %v", msg.ID, err)
		}
	}
}

// Recent returns the newest messages, oldest first.
func (s *MySQL) Recent(limit int) ([]models.Message, error) {
	rows, err := s.db.Query(
		`SELECT msg_id, type, channel, from_user, to_user, body,
			attachment_url, attachment_name, attachment_type, attachment_size, created_at
		 FROM (SELECT * FROM messages ORDER BY id DESC LIMIT ?) recent
		 ORDER BY id ASC`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	msgs := make([]models.Message, 0, limit)
	for rows.Next() {
		var m models.Message
		if err := rows.Scan(&m.ID, &m.Type, &m.Channel, &m.FromUser, &m.ToUser, &m.Ciphertext,
			&m.AttachmentURL, &m.AttachmentName, &m.AttachmentType, &m.AttachmentSize, &m.Timestamp); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// Close flushes queued writes and closes the connection pool.
func (s *MySQL) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.queue)
	}
	s.mu.Unlock()
	<-s.done
	return s.db.Close()
}
