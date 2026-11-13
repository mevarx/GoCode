package session

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mevarx/GoCode/internal/provider"
)

// SessionRecord holds the full data of a persisted session.
type SessionRecord struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Workspace string `json:"workspace"`
	// Scratchpad is agent-maintained working state that survives compaction and
	// is restored with the transcript.
	Scratchpad string             `json:"scratchpad,omitempty"`
	CreatedAt  time.Time          `json:"created_at"`
	UpdatedAt  time.Time          `json:"updated_at"`
	Messages   []provider.Message `json:"messages"`
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create session directory %s: %w", dir, err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database %s: %w", dbPath, err)
	}

	// Pin the pool to one connection: pragmas are per-connection, so a second
	// pooled connection would run without foreign_keys or busy_timeout.
	db.SetMaxOpenConns(1)

	// busy_timeout must precede the WAL switch: entering WAL needs the EXCLUSIVE
	// lock that contends most, so the wait itself stays bounded by the timeout.
	if _, err := db.Exec(`
		PRAGMA busy_timeout = 5000;
		PRAGMA foreign_keys = ON;
		PRAGMA journal_mode = WAL;
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to set sqlite pragmas: %w", err)
	}

	// The transcript can contain file contents read by tools, so it must not
	// inherit the process umask (typically 0644). WAL mode mirrors the database
	// file's mode onto its sidecars, which hold the same data; tighten the
	// database first so files SQLite creates afterwards inherit 0600.
	if err := os.Chmod(dbPath, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to tighten session database permissions: %w", err)
	}
	// Sidecars only appear after the first write, so they may be absent on a
	// fresh database; an existing pair inherited a looser mode because it was
	// created before the database was tightened, so re-check on every open.
	for _, sidecar := range []string{dbPath + "-wal", dbPath + "-shm"} {
		if err := chmodIfExists(sidecar, 0o600); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to tighten session database permissions: %w", err)
		}
	}

	store := &SessionStore{db: db}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return store, nil
}

// chmodIfExists applies mode to path, ignoring a file that does not exist yet.
func chmodIfExists(path string, mode os.FileMode) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.Chmod(path, mode)
}

func (s *SessionStore) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		provider TEXT NOT NULL,
		model TEXT NOT NULL,
		workspace TEXT NOT NULL DEFAULT '',
		scratchpad TEXT NOT NULL DEFAULT '',
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
		reasoning_content TEXT,
		created_at DATETIME NOT NULL,
		FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id);
	CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at DESC);
	`
	_, err := s.db.Exec(schema)
	if err != nil {
		return err
	}
	return s.addMissingColumns()
}

// addMissingColumns brings older databases up to the current schema; each entry is additive and idempotent.
func (s *SessionStore) addMissingColumns() error {
	migrations := []struct {
		table   string
		column  string
		ddlType string
	}{
		// reasoning_content stores model thinking output, which some
		// providers require to be replayed into history on the next turn.
		{table: "messages", column: "reasoning_content", ddlType: "TEXT"},
		// workspace scopes auto-resume to the repository the transcript came
		// from; an old database reads back as '' and therefore never matches.
		{table: "sessions", column: "workspace", ddlType: "TEXT NOT NULL DEFAULT ''"},
		// scratchpad holds agent-maintained working state that must survive
		// compaction; an old database reads it back as ''.
		{table: "sessions", column: "scratchpad", ddlType: "TEXT NOT NULL DEFAULT ''"},
	}

	for _, m := range migrations {
		exists, err := s.columnExists(m.table, m.column)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		// The table, column and type come from the literals above, never from
		// user input, so interpolating them into the DDL is safe.
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", m.table, m.column, m.ddlType)
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("failed to add column %s to %s: %w", m.column, m.table, err)
		}
	}
	return nil
}

func (s *SessionStore) columnExists(table, column string) (bool, error) {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, fmt.Errorf("failed to inspect table %s: %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var colType string
		var notNull int
		var dfltValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return false, fmt.Errorf("failed to scan column info for %s: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
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
// It keeps the historical signature for callers that do not know the
// workspace; such a session is never auto-resumed by workspace.
func (s *SessionStore) CreateSession(id, title, providerName, model string) (*SessionRecord, error) {
	return s.CreateSessionWithWorkspace(id, title, providerName, model, "")
}

// CreateSessionWithWorkspace initializes a new session bound to workspace, which
// scopes auto-resume so a transcript from another repository is never replayed.
func (s *SessionStore) CreateSessionWithWorkspace(id, title, providerName, model, workspace string) (*SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id == "" {
		id = GenerateID()
	}
	if title == "" {
		title = fmt.Sprintf("Session %s", id)
	}
	workspace = normalizeWorkspace(workspace)
	now := time.Now().UTC()

	query := `
	INSERT INTO sessions (id, title, provider, model, workspace, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	if _, err := s.db.Exec(query, id, title, providerName, model, workspace, now, now); err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	return &SessionRecord{
		ID:        id,
		Title:     title,
		Provider:  providerName,
		Model:     model,
		Workspace: workspace,
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
	INSERT INTO messages (session_id, role, content, tool_calls, tool_call_id, reasoning_content, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	if _, err := tx.Exec(insertMsg, sessionID, msg.Role, msg.Content, toolCallsJSON, msg.ToolCallID, msg.ReasoningContent, now); err != nil {
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
			newTitle := truncateTitle(strings.TrimSpace(msg.Content))
			_, _ = tx.Exec(`UPDATE sessions SET title = ? WHERE id = ?`, newTitle, sessionID)
		}
	}

	return tx.Commit()
}

// truncateTitle caps title at 40 runes, keeping 37 runes and appending "...".
// Runes, not bytes, so multibyte characters (CJK, emoji) are never split into invalid UTF-8.
func truncateTitle(title string) string {
	runes := []rune(title)
	if len(runes) <= 40 {
		return title
	}
	return string(runes[:37]) + "..."
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

	// An empty workspace means the caller does not track one (/clear), not that
	// the session lost its repository, so keep the stored binding in that case.
	upsert := `
	INSERT INTO sessions (id, title, provider, model, workspace, scratchpad, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		title = excluded.title,
		provider = excluded.provider,
		model = excluded.model,
		workspace = CASE WHEN excluded.workspace <> '' THEN excluded.workspace ELSE sessions.workspace END,
		scratchpad = CASE WHEN excluded.scratchpad <> '' THEN excluded.scratchpad ELSE sessions.scratchpad END,
		updated_at = excluded.updated_at
	`
	if _, err := tx.Exec(upsert, rec.ID, rec.Title, rec.Provider, rec.Model, normalizeWorkspace(rec.Workspace), rec.Scratchpad, rec.CreatedAt, rec.UpdatedAt); err != nil {
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
		INSERT INTO messages (session_id, role, content, tool_calls, tool_call_id, reasoning_content, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		`
		if _, err := tx.Exec(insertMsg, rec.ID, msg.Role, msg.Content, toolCallsJSON, msg.ToolCallID, msg.ReasoningContent, now); err != nil {
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

// Caller must hold s.mu (read or write).
func (s *SessionStore) getSessionLocked(id string) (*SessionRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, title, provider, model, workspace, scratchpad, created_at, updated_at
		FROM sessions
		WHERE id = ?
	`, id)

	var rec SessionRecord
	if err := row.Scan(&rec.ID, &rec.Title, &rec.Provider, &rec.Model, &rec.Workspace, &rec.Scratchpad, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get session %s: %w", id, err)
	}

	rows, err := s.db.Query(`
		SELECT role, content, tool_calls, tool_call_id, reasoning_content
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
		var reasoningContent sql.NullString
		if err := rows.Scan(&msg.Role, &msg.Content, &toolCallsJSON, &toolCallID, &reasoningContent); err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		if toolCallID.Valid {
			msg.ToolCallID = toolCallID.String
		}
		if reasoningContent.Valid {
			msg.ReasoningContent = reasoningContent.String
		}
		if toolCallsJSON.Valid && toolCallsJSON.String != "" {
			_ = json.Unmarshal([]byte(toolCallsJSON.String), &msg.ToolCalls)
		}
		rec.Messages = append(rec.Messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate messages: %w", err)
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

// GetLastSessionForWorkspace returns the most recently updated session created
// in workspace, or nil. Auto-resume is scoped this way so a transcript from
// another repository (file contents, possibly secrets) is never sent to the
// provider in the current one. An empty workspace never matches.
func (s *SessionStore) GetLastSessionForWorkspace(workspace string) (*SessionRecord, error) {
	normalized := normalizeWorkspace(workspace)
	if normalized == "" {
		return nil, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var lastID string
	err := s.db.QueryRow(
		`SELECT id FROM sessions WHERE workspace = ? ORDER BY updated_at DESC LIMIT 1`,
		normalized,
	).Scan(&lastID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query last session for workspace: %w", err)
	}

	return s.getSessionLocked(lastID)
}

// normalizeWorkspace canonicalizes a workspace path for equality checks:
// absolute, cleaned, and case-folded on Windows, where paths are
// case-insensitive. The empty string is preserved and never matches a
// real directory.
func normalizeWorkspace(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return ""
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}
	workspace = filepath.Clean(workspace)
	if runtime.GOOS == "windows" {
		workspace = strings.ToLower(workspace)
	}
	return workspace
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
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate sessions: %w", err)
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

// UpdateScratchpad persists the agent-maintained working state for a session.
// An empty value clears it, because the agent explicitly replaced it.
func (s *SessionStore) UpdateScratchpad(id, scratchpad string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`UPDATE sessions SET scratchpad = ?, updated_at = ? WHERE id = ?`, scratchpad, time.Now().UTC(), id)
	return err
}
