package session

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mevarx/GoCode/internal/provider"
)

// SessionRecord holds the full data of a persisted session.
type SessionRecord struct {
	ID        string             `json:"id"`
	Title     string             `json:"title"`
	Provider  string             `json:"provider"`
	Model     string             `json:"model"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
	Messages  []provider.Message `json:"messages"`
}

// SessionSummary holds brief metadata about a session for listing.
type SessionSummary struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	MessageCount int       `json:"message_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// SessionStore handles session persistence using SQLite.
type SessionStore struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewStore opens or creates a SQLite session database at dbPath.
func NewStore(dbPath string) (*SessionStore, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create session directory %s: %w", dir, err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database %s: %w", dbPath, err)
	}

	if _, err := db.Exec(`
		PRAGMA foreign_keys = ON;
		PRAGMA journal_mode = WAL;
		PRAGMA busy_timeout = 5000;
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to set sqlite pragmas: %w", err)
	}

	store := &SessionStore{db: db}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return store, nil
}

func (s *SessionStore) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		provider TEXT NOT NULL,
		model TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		tool_calls TEXT,
		tool_call_id TEXT,
		created_at DATETIME NOT NULL,
		FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id);
	CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at DESC);
	`
	_, err := s.db.Exec(schema)
	return err
}

// Close closes the underlying SQLite database.
func (s *SessionStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// GenerateID creates a unique session identifier.
func GenerateID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("sess_%s_%s", time.Now().Format("20060102150405"), hex.EncodeToString(b))
}

// CreateSession initializes a new session record in the database.
func (s *SessionStore) CreateSession(id, title, providerName, model string) (*SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id == "" {
		id = GenerateID()
	}
	if title == "" {
		title = fmt.Sprintf("Session %s", id)
	}
	now := time.Now().UTC()

	query := `
	INSERT INTO sessions (id, title, provider, model, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?)
	`
	if _, err := s.db.Exec(query, id, title, providerName, model, now, now); err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	return &SessionRecord{
		ID:        id,
		Title:     title,
		Provider:  providerName,
		Model:     model,
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  []provider.Message{},
	}, nil
}

// AppendMessage persists a single message to the given session and updates timestamps.
func (s *SessionStore) AppendMessage(sessionID string, msg provider.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()

	var toolCallsJSON string
	if len(msg.ToolCalls) > 0 {
		b, err := json.Marshal(msg.ToolCalls)
		if err == nil {
			toolCallsJSON = string(b)
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	insertMsg := `
	INSERT INTO messages (session_id, role, content, tool_calls, tool_call_id, created_at)
	VALUES (?, ?, ?, ?, ?, ?)
	`
	if _, err := tx.Exec(insertMsg, sessionID, msg.Role, msg.Content, toolCallsJSON, msg.ToolCallID, now); err != nil {
		return fmt.Errorf("failed to insert message: %w", err)
	}

	updateSession := `UPDATE sessions SET updated_at = ? WHERE id = ?`
	if _, err := tx.Exec(updateSession, now, sessionID); err != nil {
		return fmt.Errorf("failed to update session timestamp: %w", err)
	}

	if msg.Role == "user" && msg.Content != "" {
		var currentTitle string
		_ = tx.QueryRow(`SELECT title FROM sessions WHERE id = ?`, sessionID).Scan(&currentTitle)
		if strings.HasPrefix(currentTitle, "Session sess_") || currentTitle == "" {
			newTitle := strings.TrimSpace(msg.Content)
			if len(newTitle) > 40 {
				newTitle = newTitle[:37] + "..."
			}
			_, _ = tx.Exec(`UPDATE sessions SET title = ? WHERE id = ?`, newTitle, sessionID)
		}
	}

	return tx.Commit()
}

// SaveSession updates session metadata and overwrites all messages.
func (s *SessionStore) SaveSession(rec *SessionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	rec.UpdatedAt = now

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	upsert := `
	INSERT INTO sessions (id, title, provider, model, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		title = excluded.title,
		provider = excluded.provider,
		model = excluded.model,
		updated_at = excluded.updated_at
	`
	if _, err := tx.Exec(upsert, rec.ID, rec.Title, rec.Provider, rec.Model, rec.CreatedAt, rec.UpdatedAt); err != nil {
		return fmt.Errorf("failed to upsert session: %w", err)
	}

	if _, err := tx.Exec(`DELETE FROM messages WHERE session_id = ?`, rec.ID); err != nil {
		return fmt.Errorf("failed to clear messages: %w", err)
	}

	for _, msg := range rec.Messages {
		var toolCallsJSON string
		if len(msg.ToolCalls) > 0 {
			b, _ := json.Marshal(msg.ToolCalls)
			toolCallsJSON = string(b)
		}
		insertMsg := `
		INSERT INTO messages (session_id, role, content, tool_calls, tool_call_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		`
		if _, err := tx.Exec(insertMsg, rec.ID, msg.Role, msg.Content, toolCallsJSON, msg.ToolCallID, now); err != nil {
			return fmt.Errorf("failed to insert message: %w", err)
		}
	}

	return tx.Commit()
}

// GetSession retrieves a session record and its full message history.
func (s *SessionStore) GetSession(id string) (*SessionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getSessionLocked(id)
}

// getSessionLocked is the internal implementation of GetSession.
// Caller must hold s.mu (read or write).
func (s *SessionStore) getSessionLocked(id string) (*SessionRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, title, provider, model, created_at, updated_at
		FROM sessions
		WHERE id = ?
	`, id)

	var rec SessionRecord
	if err := row.Scan(&rec.ID, &rec.Title, &rec.Provider, &rec.Model, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get session %s: %w", id, err)
	}

	rows, err := s.db.Query(`
		SELECT role, content, tool_calls, tool_call_id
		FROM messages
		WHERE session_id = ?
		ORDER BY id ASC
	`, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get messages: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var msg provider.Message
		var toolCallsJSON sql.NullString
		var toolCallID sql.NullString
		if err := rows.Scan(&msg.Role, &msg.Content, &toolCallsJSON, &toolCallID); err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		if toolCallID.Valid {
			msg.ToolCallID = toolCallID.String
		}
		if toolCallsJSON.Valid && toolCallsJSON.String != "" {
			_ = json.Unmarshal([]byte(toolCallsJSON.String), &msg.ToolCalls)
		}
		rec.Messages = append(rec.Messages, msg)
	}

	return &rec, nil
}

// GetLastSession returns the most recently updated session, or nil if no sessions exist.
func (s *SessionStore) GetLastSession() (*SessionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var lastID string
	err := s.db.QueryRow(`SELECT id FROM sessions ORDER BY updated_at DESC LIMIT 1`).Scan(&lastID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query last session: %w", err)
	}

	return s.getSessionLocked(lastID)
}

// ListSessions returns a summary of recent sessions up to limit.
func (s *SessionStore) ListSessions(limit int) ([]SessionSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 20
	}

	query := `
	SELECT s.id, s.title, s.provider, s.model, s.created_at, s.updated_at, COUNT(m.id) as message_count
	FROM sessions s
	LEFT JOIN messages m ON s.id = m.session_id
	GROUP BY s.id
	ORDER BY s.updated_at DESC
	LIMIT ?
	`
	rows, err := s.db.Query(query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	defer rows.Close()

	var summaries []SessionSummary
	for rows.Next() {
		var sum SessionSummary
		if err := rows.Scan(&sum.ID, &sum.Title, &sum.Provider, &sum.Model, &sum.CreatedAt, &sum.UpdatedAt, &sum.MessageCount); err != nil {
			return nil, fmt.Errorf("failed to scan session summary: %w", err)
		}
		summaries = append(summaries, sum)
	}

	return summaries, nil
}

// DeleteSession deletes a session and all its messages.
func (s *SessionStore) DeleteSession(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete session %s: %w", id, err)
	}
	return nil
}

// UpdateModel updates the model and provider of an existing session.
func (s *SessionStore) UpdateModel(id, providerName, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`UPDATE sessions SET provider = ?, model = ?, updated_at = ? WHERE id = ?`, providerName, model, time.Now().UTC(), id)
	return err
}
