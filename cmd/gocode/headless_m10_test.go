package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/provider"
)

// headless contracts: queued user records run in FIFO order through the
// dispatcher, a cancel record settles every dropped record with a terminal
// turn_done (piped input never hangs), and the JSONL schema stays
// byte-identical — settlements reuse the existing turn_done record with no
// new fields. All waits bounded; fakes only.

// m10Writer is a concurrency-safe output sink that signals every turn_done
// record, so a pipe writer can wait for cancel settlement deterministically.
type m10Writer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	turnDones chan struct{}
}

func newM10Writer() *m10Writer {
	return &m10Writer{turnDones: make(chan struct{}, 16)}
}

func (w *m10Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if bytes.Contains(p, []byte(`"type":"turn_done"`)) {
		select {
		case w.turnDones <- struct{}{}:
		default:
		}
	}
	return n, err
}

func (w *m10Writer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func (w *m10Writer) waitTurnDones(n int) bool {
	deadline := time.After(3 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case <-w.turnDones:
		case <-deadline:
			return false
		}
	}
	return true
}

// Queued user records run in order after the running turn, each closing with
// its own turn_done — the dispatcher replaced the private pending slice
// without changing piped-input semantics.
func TestHeadlessQueuedUserRecordsRunInOrder(t *testing.T) {
	p := &headlessBlockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	tool := &headlessFakeTool{name: "probe", output: "ok"}
	loop := newHeadlessLoop(t, p, nil, tool)

	pr, pw := io.Pipe()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		_, _ = pw.Write([]byte(`{"type":"user","text":"one"}` + "\n"))
		select {
		case <-p.started:
		case <-time.After(3 * time.Second):
			close(p.release)
			_ = pw.Close()
			return
		}
		_, _ = pw.Write([]byte(`{"type":"user","text":"two"}` + "\n"))
		close(p.release)
		_, _ = pw.Write([]byte(`{"type":"exit"}` + "\n"))
		_ = pw.Close()
	}()

	var out bytes.Buffer
	if err := runHeadless(context.Background(), pr, &out, loop); err != nil {
		t.Fatalf("runHeadless: %v", err)
	}
	<-writerDone

	records := decodeHeadlessRecords(t, out.String())
	if got := countRecords(records, "turn_done"); got != 2 {
		t.Fatalf("turn_done count = %d, want 2 (%q)", got, out.String())
	}
	if got := countRecords(records, "error"); got != 0 {
		t.Fatalf("error records = %d, want 0 (%q)", got, out.String())
	}
	p.mu.Lock()
	rounds := p.round
	p.mu.Unlock()
	if rounds != 3 {
		t.Errorf("provider rounds = %d, want 3 (tool round + answer + queued turn)", rounds)
	}
	var users []string
	for _, msg := range loop.Session.History() {
		if msg.Role == "user" && !strings.HasPrefix(msg.Content, "[steering]") {
			users = append(users, msg.Content)
		}
	}
	if len(users) != 2 || users[0] != "one" || users[1] != "two" {
		t.Errorf("user prompt order = %v, want [one two]", users)
	}
}

// A cancel record raises the mark, cancels the running turn cooperatively and
// settles EVERY queued record with a terminal turn_done. The driver's
// per-prompt accounting closes (3 prompts → 3 turn_done), the process exits
// cleanly, and the queued prompts never reach the provider.
func TestHeadlessCancelSettlesQueuedRecords(t *testing.T) {
	p := &headlessBlockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	tool := &headlessFakeTool{name: "probe", output: "ok"}
	loop := newHeadlessLoop(t, p, nil, tool)
	w := newM10Writer()

	pr, pw := io.Pipe()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		_, _ = pw.Write([]byte(`{"type":"user","text":"one"}` + "\n"))
		select {
		case <-p.started:
		case <-time.After(3 * time.Second):
			close(p.release)
			_ = pw.Close()
			return
		}
		_, _ = pw.Write([]byte(`{"type":"user","text":"two"}` + "\n"))
		_, _ = pw.Write([]byte(`{"type":"user","text":"three"}` + "\n"))
		_, _ = pw.Write([]byte(`{"type":"cancel"}` + "\n"))
		// Wait until both queued records are settled BEFORE releasing the
		// stream, so the cancel deterministically lands mid-turn. On a
		// missed settlement, unblock the loop so the test fails fast.
		if !w.waitTurnDones(2) {
			close(p.release)
			_ = pw.Close()
			return
		}
		close(p.release)
		_, _ = pw.Write([]byte(`{"type":"exit"}` + "\n"))
		_ = pw.Close()
	}()

	if err := runHeadless(context.Background(), pr, w, loop); err != nil {
		t.Fatalf("runHeadless after cancel: %v", err)
	}
	<-writerDone

	records := decodeHeadlessRecords(t, w.String())
	if got := countRecords(records, "turn_done"); got != 3 {
		t.Fatalf("turn_done count = %d, want one per accepted prompt (3) (%q)", got, w.String())
	}
	if got := countRecords(records, "error"); got != 0 {
		t.Fatalf("error records = %d, want 0 — a user cancel is not a protocol failure (%q)", got, w.String())
	}
	p.mu.Lock()
	rounds := p.round
	p.mu.Unlock()
	if rounds != 1 {
		t.Errorf("provider rounds = %d, want 1 (queued turns never ran)", rounds)
	}
	// Settled rows are persisted; the cancelled stream's declared tool call
	// keeps a paired result.
	var notices int
	var paired bool
	for _, msg := range loop.Session.History() {
		if msg.Role == "assistant" && msg.Content == "[Turn cancelled before it started.]" {
			notices++
		}
		if msg.Role == "tool" && msg.ToolCallID == "c1" && strings.Contains(msg.Content, "interrupted") {
			paired = true
		}
	}
	if notices != 2 {
		t.Errorf("cancelled turn rows = %d, want 2", notices)
	}
	if !paired {
		t.Error("the cancelled turn's tool call has no interrupted result")
	}
	if loop.QueuedTurnCount() != 0 {
		t.Errorf("queue survived cancel: %d", loop.QueuedTurnCount())
	}
}

// Schema contract: settlements reuse the EXISTING turn_done record — no new
// record types, no new fields. A settled turn_done carries only "type"; a
// measured one carries the unchanged metrics object.
func TestHeadlessCancelSchemaIsAdditiveOnly(t *testing.T) {
	p := &headlessBlockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	loop := newHeadlessLoop(t, p, nil, &headlessFakeTool{name: "probe", output: "ok"})
	w := newM10Writer()

	pr, pw := io.Pipe()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		_, _ = pw.Write([]byte(`{"type":"user","text":"one"}` + "\n"))
		select {
		case <-p.started:
		case <-time.After(3 * time.Second):
			close(p.release)
			_ = pw.Close()
			return
		}
		_, _ = pw.Write([]byte(`{"type":"user","text":"two"}` + "\n"))
		_, _ = pw.Write([]byte(`{"type":"cancel"}` + "\n"))
		if !w.waitTurnDones(1) {
			close(p.release)
			_ = pw.Close()
			return
		}
		close(p.release)
		_, _ = pw.Write([]byte(`{"type":"exit"}` + "\n"))
		_ = pw.Close()
	}()

	if err := runHeadless(context.Background(), pr, w, loop); err != nil {
		t.Fatalf("runHeadless: %v", err)
	}
	<-writerDone

	known := map[string]bool{
		"delta": true, "notice": true, "tool_start": true, "tool_delta": true,
		"tool_result": true, "error": true, "turn_done": true,
	}
	var turnDones []map[string]any
	for _, rec := range decodeHeadlessRecords(t, w.String()) {
		typ, _ := rec["type"].(string)
		if !known[typ] {
			t.Fatalf("unknown record type %q — schema must stay additive-only", typ)
		}
		if typ == "turn_done" {
			turnDones = append(turnDones, rec)
		}
	}
	if len(turnDones) != 2 {
		t.Fatalf("turn_done count = %d, want 2", len(turnDones))
	}
	// The settlement (written at cancel time) precedes the active turn's own.
	if len(turnDones[0]) != 1 {
		t.Errorf("settled turn_done = %#v, want only the type field", turnDones[0])
	}
	for key := range turnDones[1] {
		if key != "type" && key != "metrics" {
			t.Errorf("turn_done gained field %q — schema must stay byte-identical", key)
		}
	}
	if _, ok := turnDones[1]["metrics"].(map[string]any); !ok {
		t.Errorf("measured turn_done lost its metrics object: %#v", turnDones[1])
	}
}

// A cancel while idle settles nothing and never poisons later prompts: the
// next accepted sequence is above the mark and runs normally.
func TestHeadlessCancelIdleThenPromptRuns(t *testing.T) {
	p := &headlessScriptProvider{scripts: [][]provider.StreamChunk{{
		{Delta: "hello"}, {Done: true, FinishReason: "stop"},
	}}}
	loop := newHeadlessLoop(t, p, nil)

	input := strings.Join([]string{
		`{"type":"cancel"}`,
		`{"type":"user","text":"hi"}`,
		`{"type":"exit"}`,
		"",
	}, "\n")

	var out bytes.Buffer
	if err := runHeadless(context.Background(), strings.NewReader(input), &out, loop); err != nil {
		t.Fatalf("runHeadless: %v", err)
	}
	records := decodeHeadlessRecords(t, out.String())
	if got := countRecords(records, "turn_done"); got != 1 {
		t.Fatalf("turn_done count = %d, want 1 (%q)", got, out.String())
	}
	if got := countRecords(records, "error"); got != 0 {
		t.Errorf("idle cancel produced %d error records", got)
	}
	if got := countRecords(records, "delta"); got != 1 {
		t.Errorf("post-cancel prompt streamed %d deltas, want 1", got)
	}
	if loop.CancelMark() != 0 {
		t.Errorf("idle cancel raised the mark to %d with no accepts", loop.CancelMark())
	}
}

// Steering keeps its separate lane under the dispatcher: a steer record
// mid-turn still folds at the round boundary while user records queue.
func TestHeadlessSteerLaneUnchangedWithQueue(t *testing.T) {
	p := &headlessBlockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	loop := newHeadlessLoop(t, p, nil, &headlessFakeTool{name: "probe", output: "ok"})

	pr, pw := io.Pipe()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		_, _ = pw.Write([]byte(`{"type":"user","text":"start"}` + "\n"))
		select {
		case <-p.started:
		case <-time.After(3 * time.Second):
			close(p.release)
			_ = pw.Close()
			return
		}
		_, _ = pw.Write([]byte(`{"type":"steer","text":"focus"}` + "\n"))
		_, _ = pw.Write([]byte(`{"type":"user","text":"later"}` + "\n"))
		close(p.release)
		_, _ = pw.Write([]byte(`{"type":"exit"}` + "\n"))
		_ = pw.Close()
	}()

	var out bytes.Buffer
	if err := runHeadless(context.Background(), pr, &out, loop); err != nil {
		t.Fatalf("runHeadless: %v", err)
	}
	<-writerDone

	records := decodeHeadlessRecords(t, out.String())
	if got := countRecords(records, "turn_done"); got != 2 {
		t.Fatalf("turn_done count = %d, want 2 (%q)", got, out.String())
	}
	var steered bool
	for _, rec := range records {
		if rec["type"] == "notice" && rec["text"] == agent.SteeringNotice {
			steered = true
		}
	}
	if !steered {
		t.Error("steering notice missing — the steer lane must survive dispatch wiring")
	}
	if !hasHeadlessUserMessage(loop, "[steering] focus") {
		t.Error("steering never reached history")
	}
	if !hasHeadlessUserMessage(loop, "later") {
		t.Error("the queued user record never ran")
	}
}

func hasHeadlessUserMessage(loop *agent.AgentLoop, content string) bool {
	for _, msg := range loop.Session.History() {
		if msg.Role == "user" && msg.Content == content {
			return true
		}
	}
	return false
}
