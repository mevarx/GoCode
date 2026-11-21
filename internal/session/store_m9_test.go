package session

import (
	"strconv"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
)

// replay reads from the summary anchor onward, the summary row is
// relabelled to a user turn, and pre-anchor rows are never deleted.
func TestSummaryAnchorReplayAndRetention(t *testing.T) {
	st, err := NewStore(t.TempDir() + "/anchor.db")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()
	if _, err := st.CreateSession("s1", "Anchor", "openai", "gpt-4o"); err != nil {
		t.Fatalf("create: %v", err)
	}

	mustAppend := func(role, content string) {
		t.Helper()
		if err := st.AppendMessage("s1", provider.Message{Role: role, Content: content}); err != nil {
			t.Fatalf("append %s: %v", content, err)
		}
	}
	mustAppend("user", "old1")
	mustAppend("assistant", "old2")
	rowID, err := st.AppendSummaryMessage("s1", provider.Message{Role: "assistant", Content: "SUMMARY"})
	if err != nil {
		t.Fatalf("append summary: %v", err)
	}
	mustAppend("assistant", "ack")
	mustAppend("user", "recent")
	if err := st.SetSummaryAnchor("s1", strconv.FormatInt(rowID, 10)); err != nil {
		t.Fatalf("set anchor: %v", err)
	}

	loaded, err := st.GetSession("s1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(loaded.Messages) != 3 {
		t.Fatalf("expected 3 replayed messages (summary, ack, recent), got %d", len(loaded.Messages))
	}
	if loaded.Messages[0].Role != "user" || loaded.Messages[0].Content != "SUMMARY" {
		t.Errorf("summary should relabel to user, got %+v", loaded.Messages[0])
	}
	if loaded.Messages[2].Content != "recent" {
		t.Errorf("tail did not replay: %+v", loaded.Messages)
	}

	// Pre-anchor rows are retained in the database (nothing deleted).
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, "s1").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 5 {
		t.Errorf("expected 5 raw rows retained (2 pre-anchor + summary + ack + recent), got %d", n)
	}

	// A full rewrite (SaveSession) inlines the transcript and clears the
	// anchor, so the next replay returns the whole compacted history.
	if err := st.SaveSession(&SessionRecord{ID: "s1", Title: "Anchor", Provider: "openai", Model: "gpt-4o", Messages: loaded.Messages}); err != nil {
		t.Fatalf("save: %v", err)
	}
	var anchor string
	if err := st.db.QueryRow(`SELECT summary_message_id FROM sessions WHERE id = ?`, "s1").Scan(&anchor); err != nil {
		t.Fatalf("read anchor: %v", err)
	}
	if anchor != "" {
		t.Errorf("SaveSession should clear the anchor, got %q", anchor)
	}
}
