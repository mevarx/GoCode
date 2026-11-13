package agent

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
)

// assertHistoryInvariant checks the wire contract enforced by EnsureToolPairing:
// one system message at index 0, every tool result directly after the assistant
// message that declared its call, exactly one result per declared call in call
// order, and no orphaned tool messages.
func assertHistoryInvariant(t *testing.T, label string, history []provider.Message) {
	t.Helper()

	for i, msg := range history {
		if msg.Role == "system" && i != 0 {
			t.Fatalf("%s: system message at index %d, want index 0", label, i)
		}
	}
	for i := 1; i < len(history); i++ {
		if history[i].Role == "system" {
			t.Fatalf("%s: duplicate system message at index %d", label, i)
		}
	}

	for i := 0; i < len(history); i++ {
		msg := history[i]
		if msg.Role == "tool" {
			t.Fatalf("%s: orphan tool message at index %d (call id %q)", label, i, msg.ToolCallID)
		}
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			continue
		}

		j := i + 1
		for _, call := range msg.ToolCalls {
			if j >= len(history) {
				t.Fatalf("%s: assistant at %d is missing a result for call %q", label, i, call.ID)
			}
			res := history[j]
			if res.Role != "tool" {
				t.Fatalf("%s: call %q of assistant %d followed by %q, want tool", label, call.ID, i, res.Role)
			}
			if res.ToolCallID != call.ID {
				t.Fatalf("%s: result %q at %d does not match call %q", label, res.ToolCallID, j, call.ID)
			}
			j++
		}
		if j < len(history) && history[j].Role == "tool" {
			t.Fatalf("%s: extra tool result at %d after the calls of assistant %d", label, j, i)
		}
		i = j - 1
	}
}

// assistantFingerprints identifies assistant messages so a test can prove that
// a transformation dropped no assistant text and fabricated none.
func assistantFingerprints(history []provider.Message) []string {
	var out []string
	for _, msg := range history {
		if msg.Role != "assistant" {
			continue
		}
		var b strings.Builder
		b.WriteString(msg.Content)
		b.WriteString("\x00")
		b.WriteString(msg.ReasoningContent)
		for _, call := range msg.ToolCalls {
			b.WriteString("\x00")
			b.WriteString(call.ID)
			b.WriteString("\x00")
			b.WriteString(call.Name)
			b.WriteString("\x00")
			b.WriteString(string(call.Args))
		}
		out = append(out, b.String())
	}
	return out
}

func assertAssistantSuffix(t *testing.T, label string, full, retained []provider.Message) {
	t.Helper()
	want := assistantFingerprints(full)
	got := assistantFingerprints(retained)
	if len(got) > len(want) {
		t.Fatalf("%s: retained %d assistant messages, input had %d", label, len(got), len(want))
	}
	tail := want[len(want)-len(got):]
	for i := range got {
		if got[i] != tail[i] {
			t.Fatalf("%s: assistant message %d was altered: got %q want %q", label, i, got[i], tail[i])
		}
	}
}

func TestEnsureToolPairing_EmptyHistory(t *testing.T) {
	if got := EnsureToolPairing(nil); len(got) != 0 {
		t.Fatalf("expected empty result, got %d messages", len(got))
	}
	if got := EnsureToolPairing([]provider.Message{}); len(got) != 0 {
		t.Fatalf("expected empty result, got %d messages", len(got))
	}
}

func TestEnsureToolPairing_LeadingToolResult(t *testing.T) {
	history := []provider.Message{
		{Role: "tool", Content: "stale", ToolCallID: "call_1"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}
	got := EnsureToolPairing(history)
	if len(got) != 2 || got[0].Role != "user" || got[1].Role != "assistant" {
		t.Fatalf("orphan leading tool result not dropped: %+v", got)
	}
	assertHistoryInvariant(t, "leading tool result", got)
}

func TestEnsureToolPairing_AssistantCallWithoutResult(t *testing.T) {
	history := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "thinking", ToolCalls: []provider.ToolCall{
			{ID: "call_a", Name: "shell_exec", Args: []byte(`{}`)},
			{ID: "call_b", Name: "code_search", Args: []byte(`{}`)},
		}},
		{Role: "tool", Content: "ok", ToolCallID: "call_a"},
	}
	got := EnsureToolPairing(history)
	assertHistoryInvariant(t, "assistant call without result", got)

	if len(got) != 5 {
		t.Fatalf("expected 5 messages, got %d: %+v", len(got), got)
	}
	if got[2].Content != "thinking" {
		t.Fatalf("assistant text content was lost: %+v", got[2])
	}
	if got[3].ToolCallID != "call_a" || got[3].Content != "ok" {
		t.Fatalf("real result not preserved: %+v", got[3])
	}
	if got[4].ToolCallID != "call_b" || got[4].Content != syntheticToolResult {
		t.Fatalf("missing synthetic result for call_b: %+v", got[4])
	}
}

func TestEnsureToolPairing_ToolResultWithoutCall(t *testing.T) {
	history := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "answer"},
		{Role: "tool", Content: "unexpected", ToolCallID: "call_x"},
	}
	got := EnsureToolPairing(history)
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(got), got)
	}
	for _, msg := range got {
		if msg.Role == "tool" {
			t.Fatalf("orphan result survived: %+v", got)
		}
	}
	assertHistoryInvariant(t, "result without call", got)
}

func TestEnsureToolPairing_InterleavedCallsAndResults(t *testing.T) {
	history := []provider.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "calling", ToolCalls: []provider.ToolCall{
			{ID: "call_a", Name: "t", Args: []byte(`{}`)},
			{ID: "call_b", Name: "t", Args: []byte(`{}`)},
			{ID: "call_c", Name: "t", Args: []byte(`{}`)},
		}},
		{Role: "tool", Content: "res-a", ToolCallID: "call_a"},
		{Role: "tool", Content: "res-c", ToolCallID: "call_c"},
	}
	got := EnsureToolPairing(history)
	assertHistoryInvariant(t, "interleaved calls", got)

	if len(got) != 5 {
		t.Fatalf("expected user + assistant + 3 results, got %d: %+v", len(got), got)
	}
	wantIDs := []string{"call_a", "call_b", "call_c"}
	wantContent := []string{"res-a", syntheticToolResult, "res-c"}
	for i, id := range wantIDs {
		res := got[i+2]
		if res.ToolCallID != id || res.Content != wantContent[i] {
			t.Fatalf("result %d = {%q,%q}, want {%q,%q}", i, res.ToolCallID, res.Content, id, wantContent[i])
		}
	}
}

func TestEnsureToolPairing_DuplicateResultsCollapsed(t *testing.T) {
	history := []provider.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "call_a", Name: "t", Args: []byte(`{}`)},
		}},
		{Role: "tool", Content: "first", ToolCallID: "call_a"},
		{Role: "tool", Content: "second", ToolCallID: "call_a"},
	}
	got := EnsureToolPairing(history)
	assertHistoryInvariant(t, "duplicate results", got)
	if len(got) != 3 || got[2].Content != "first" {
		t.Fatalf("expected exactly the first result for call_a, got %+v", got)
	}
}

func TestEnsureToolPairing_ValidHistoryUnchanged(t *testing.T) {
	history := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "calling", ToolCalls: []provider.ToolCall{
			{ID: "call_a", Name: "t", Args: []byte(`{}`)},
			{ID: "call_b", Name: "t", Args: []byte(`{}`)},
		}},
		{Role: "tool", Content: "res-a", ToolCallID: "call_a"},
		{Role: "tool", Content: "res-b", ToolCallID: "call_b"},
		{Role: "assistant", Content: "done"},
	}
	got := EnsureToolPairing(history)
	if !reflect.DeepEqual(got, history) {
		t.Fatalf("valid history was rewritten:\n got %+v\nwant %+v", got, history)
	}
}

func TestEnsureToolPairing_SystemNotFirst(t *testing.T) {
	history := []provider.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
		{Role: "system", Content: "sys"},
	}
	got := EnsureToolPairing(history)
	assertHistoryInvariant(t, "system not first", got)
	if len(got) != 3 || got[0].Role != "system" || got[0].Content != "sys" {
		t.Fatalf("system message not moved to index 0: %+v", got)
	}
}

func TestEnsureToolPairing_DuplicateSystem(t *testing.T) {
	history := []provider.Message{
		{Role: "system", Content: "one"},
		{Role: "user", Content: "hi"},
		{Role: "system", Content: "two"},
	}
	got := EnsureToolPairing(history)
	assertHistoryInvariant(t, "duplicate system", got)
	if len(got) != 2 {
		t.Fatalf("expected merged system + user, got %+v", got)
	}
	if got[0].Content != "one\n\ntwo" {
		t.Fatalf("system prompts not merged in order: %q", got[0].Content)
	}
}

func TestEnsureToolPairing_DoesNotMutateInput(t *testing.T) {
	history := []provider.Message{
		{Role: "tool", Content: "stale", ToolCallID: "call_1"},
		{Role: "assistant", Content: "calling", ToolCalls: []provider.ToolCall{
			{ID: "call_2", Name: "t", Args: []byte(`{}`)},
		}},
	}
	before := append([]provider.Message(nil), history...)
	EnsureToolPairing(history)
	if !reflect.DeepEqual(history, before) {
		t.Fatalf("input history was mutated: %+v", history)
	}
}

func TestTrimHistory_KeepsWholeTurns(t *testing.T) {
	history := []provider.Message{{Role: "system", Content: "sys"}}
	for i := range 5 {
		history = append(history,
			provider.Message{Role: "user", Content: fmt.Sprintf("u%d", i)},
			provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{
				{ID: fmt.Sprintf("call_%d", i), Name: "t", Args: []byte(`{}`)},
			}},
			provider.Message{Role: "tool", Content: fmt.Sprintf("r%d", i), ToolCallID: fmt.Sprintf("call_%d", i)},
			provider.Message{Role: "assistant", Content: fmt.Sprintf("a%d", i)},
		)
	}

	got := trimHistory(history, 2)
	assertHistoryInvariant(t, "trim whole turns", got)

	if got[0].Role != "system" {
		t.Fatalf("system message lost: %+v", got[0])
	}
	if len(got) != 1+2*4 {
		t.Fatalf("expected system + 2 turns (9 messages), got %d: %+v", len(got), got)
	}
	if got[1].Role != "user" || got[1].Content != "u3" {
		t.Fatalf("first retained turn should start at u3, got %+v", got[1])
	}
	if got[len(got)-1].Content != "a4" {
		t.Fatalf("last retained assistant message wrong: %+v", got[len(got)-1])
	}
}

func TestTrimHistory_InFlightToolCallsLeftAlone(t *testing.T) {
	history := []provider.Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "call_a", Name: "t", Args: []byte(`{}`)},
			{ID: "call_b", Name: "t", Args: []byte(`{}`)},
		}},
		{Role: "tool", Content: "res-a", ToolCallID: "call_a"},
	}

	got := trimHistory(history, 1)
	if len(got) != len(history) {
		t.Fatalf("in-flight tail was altered: %+v", got)
	}
	for _, msg := range got {
		if msg.Role == "tool" && msg.Content == syntheticToolResult {
			t.Fatalf("synthetic result injected into an unfinished call: %+v", got)
		}
	}

	// Once the second result lands the history is settled and still valid.
	history = append(history, provider.Message{Role: "tool", Content: "res-b", ToolCallID: "call_b"})
	got = trimHistory(history, 1)
	assertHistoryInvariant(t, "settled trim", got)
	if len(got) != len(history) {
		t.Fatalf("settled history changed unexpectedly: %+v", got)
	}
}

// A live turn legitimately lacks results, but results it does have must not be
// replaced by synthetics and undeclared results must not survive the trim.
func TestTrimHistory_LiveTurnKeepsOnlyDeclaredResults(t *testing.T) {
	history := []provider.Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "call_a", Name: "t", Args: []byte(`{}`)},
			{ID: "call_b", Name: "t", Args: []byte(`{}`)},
			{ID: "call_c", Name: "t", Args: []byte(`{}`)},
		}},
		{Role: "tool", Content: "res-a", ToolCallID: "call_a"},
		{Role: "tool", Content: "bogus", ToolCallID: "call_x"},
	}

	got := trimHistory(history, 1)
	if len(got) != 3 {
		t.Fatalf("expected user, assistant, one real result; got %+v", got)
	}
	if got[1].Role != "assistant" || got[2].ToolCallID != "call_a" || got[2].Content != "res-a" {
		t.Fatalf("live turn results were rewritten: %+v", got)
	}
	for _, msg := range got {
		if msg.Role == "tool" && (msg.Content == syntheticToolResult || msg.ToolCallID == "call_x") {
			t.Fatalf("live trim injected or kept an undeclared result: %+v", got)
		}
	}
}

// Hoisting an old malformed assistant call to the end of the window must not
// disguise it as a live turn: the settled prefix still has to be repaired.
func TestTrimHistory_LiveTurnRepairsSettledPrefix(t *testing.T) {
	history := []provider.Message{
		{Role: "tool", Content: "stale", ToolCallID: "call_old"},
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "call_a", Name: "t", Args: []byte(`{}`)},
			{ID: "call_b", Name: "t", Args: []byte(`{}`)},
		}},
		{Role: "tool", Content: "res-a", ToolCallID: "call_a"},
	}

	got := trimHistory(history, 5)
	if len(got) != 3 || got[0].Role != "user" || got[1].Role != "assistant" || got[2].Content != "res-a" {
		t.Fatalf("unexpected live-trim result: %+v", got)
	}
	for _, msg := range got {
		if msg.Content == "stale" {
			t.Fatalf("orphan in the settled prefix survived the trim: %+v", got)
		}
	}
}

func TestTrimHistory_RepairsOrphans(t *testing.T) {
	history := []provider.Message{
		{Role: "tool", Content: "stale", ToolCallID: "call_0"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "calling", ToolCalls: []provider.ToolCall{
			{ID: "call_1", Name: "t", Args: []byte(`{}`)},
		}},
		{Role: "assistant", Content: "after"},
	}
	got := trimHistory(history, 40)
	assertHistoryInvariant(t, "repair orphans", got)
	if len(got) != 4 {
		t.Fatalf("expected user, assistant, synthetic result, assistant text, got %+v", got)
	}
	if got[0].Role != "user" || got[1].Role != "assistant" || got[2].ToolCallID != "call_1" {
		t.Fatalf("unexpected repaired history: %+v", got)
	}
}

func TestSession_AddMessageTrimsAtTurnBoundary(t *testing.T) {
	sess := NewSession("m")
	sess.AddMessage(provider.Message{Role: "system", Content: "sys"})
	for i := range 130 {
		id := fmt.Sprintf("call_%d", i)
		sess.AddMessage(provider.Message{Role: "user", Content: fmt.Sprintf("u%d", i)})
		sess.AddMessage(provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: id, Name: "t", Args: []byte(`{}`)},
		}})
		sess.AddMessage(provider.Message{Role: "tool", Content: fmt.Sprintf("r%d", i), ToolCallID: id})
		sess.AddMessage(provider.Message{Role: "assistant", Content: fmt.Sprintf("a%d", i)})
	}

	history := sess.History()
	if len(history) > maxHistoryMessages {
		t.Fatalf("safety cap failed: %d messages > %d", len(history), maxHistoryMessages)
	}
	assertHistoryInvariant(t, "add message trim", history)
	if history[0].Role != "system" {
		t.Fatalf("system message lost: %+v", history[0])
	}
	if history[1].Role != "user" {
		t.Fatalf("trimmed history does not start at a turn boundary: %+v", history[1])
	}
	if history[len(history)-1].Content != "a129" {
		t.Fatalf("latest turn was lost: %+v", history[len(history)-1])
	}
}

// AddMessage can cross the 500-message threshold while a tool call is still in
// flight. The trim must not synthesize results for it, or the real results that
// arrive next become orphans.
func TestSession_AddMessageMidTurnKeepsRealResults(t *testing.T) {
	sess := NewSession("m")
	loaded := []provider.Message{{Role: "system", Content: "sys"}}
	for i := range 249 {
		loaded = append(loaded,
			provider.Message{Role: "user", Content: fmt.Sprintf("u%d", i)},
			provider.Message{Role: "assistant", Content: fmt.Sprintf("a%d", i)},
		)
	}
	sess.LoadMessages(loaded) // 499 messages

	sess.AddMessage(provider.Message{Role: "user", Content: "live turn"}) // 500
	sess.AddMessage(provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{
		{ID: "call_a", Name: "t", Args: []byte(`{}`)},
		{ID: "call_b", Name: "t", Args: []byte(`{}`)},
	}}) // 501 -> trim while in flight

	history := sess.History()
	if len(history) > maxHistoryMessages {
		t.Fatalf("safety cap failed: %d messages > %d", len(history), maxHistoryMessages)
	}
	if history[0].Role != "system" {
		t.Fatalf("system message lost: %+v", history[0])
	}
	for _, msg := range history {
		if msg.Role == "tool" {
			t.Fatalf("synthetic result injected into a live turn: %+v", msg)
		}
	}
	last := history[len(history)-1]
	if last.Role != "assistant" || len(last.ToolCalls) != 2 {
		t.Fatalf("live turn tail was altered: %+v", last)
	}

	sess.AddMessage(provider.Message{Role: "tool", Content: "real-a", ToolCallID: "call_a"})
	sess.AddMessage(provider.Message{Role: "tool", Content: "real-b", ToolCallID: "call_b"})

	got := NewContextManager(8192).Truncate(sess.History())
	assertHistoryInvariant(t, "live turn real results", got)

	var results []string
	for _, msg := range got {
		if msg.Role == "tool" {
			results = append(results, msg.Content)
		}
	}
	if !reflect.DeepEqual(results, []string{"real-a", "real-b"}) {
		t.Fatalf("real results replaced by synthetics: %v", results)
	}
}

func TestTruncate_RepairsOrphansUnderBudget(t *testing.T) {
	cm := NewContextManager(100000)
	history := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "tool", Content: "stale", ToolCallID: "call_0"},
		{Role: "assistant", Content: "answer"},
		{Role: "assistant", Content: "calling", ToolCalls: []provider.ToolCall{
			{ID: "call_1", Name: "t", Args: []byte(`{}`)},
		}},
	}
	got := cm.Truncate(history)
	assertHistoryInvariant(t, "truncate under budget", got)
	if len(got) != 5 {
		t.Fatalf("expected repaired history with synthetic result (5 messages), got %+v", got)
	}
	if got[len(got)-1].ToolCallID != "call_1" || got[len(got)-1].Content != syntheticToolResult {
		t.Fatalf("missing synthetic result: %+v", got[len(got)-1])
	}
}

func TestTruncate_KeepsCommittedToolPairs(t *testing.T) {
	cm := NewContextManager(30)
	history := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "first turn with a long question that burns the budget"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "call_1", Name: "shell_exec", Args: []byte(`{"command":"ls"}`)},
		}},
		{Role: "tool", Content: "file1.txt", ToolCallID: "call_1"},
		{Role: "assistant", Content: "here are the files"},
		{Role: "user", Content: "second turn"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "call_2", Name: "code_search", Args: []byte(`{"query":"test"}`)},
		}},
		{Role: "tool", Content: "match found", ToolCallID: "call_2"},
		{Role: "assistant", Content: "found it"},
	}
	got := cm.Truncate(history)
	assertHistoryInvariant(t, "truncate keeps pairs", got)
	if len(got) > 1 && got[1].Role == "tool" {
		t.Fatalf("history starts with an orphaned tool message: %+v", got)
	}
}

func TestCompact_SatisfiesInvariantWhenBoundarySplitsToolPair(t *testing.T) {
	cm := NewContextManager(8192)
	history := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "turn 1"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "call_1", Name: "t", Args: []byte(`{}`)},
		}},
		{Role: "user", Content: "turn 2"},
		{Role: "tool", Content: "stranded result", ToolCallID: "call_1"},
		{Role: "assistant", Content: "done"},
	}

	got := cm.Compact(history, 1)
	assertHistoryInvariant(t, "compact split pair", got)

	if len(got) < 3 || got[0].Role != "system" || got[1].Role != "user" || got[2].Role != "assistant" {
		t.Fatalf("expected system + summary pair, got %+v", got)
	}
	if !strings.Contains(got[1].Content, "compacted") {
		t.Fatalf("summary message missing: %+v", got[1])
	}
	for _, msg := range got {
		if msg.Role == "tool" {
			t.Fatalf("compaction kept a stranded tool result: %+v", got)
		}
	}
	if got[len(got)-1].Content != "done" {
		t.Fatalf("retained turn lost: %+v", got[len(got)-1])
	}
}

func randomHistory(rng *rand.Rand) []provider.Message {
	ids := []string{"call_0", "call_1", "call_2", "call_3"}
	n := rng.IntN(40)
	history := make([]provider.Message, 0, n)
	for range n {
		switch rng.IntN(10) {
		case 0:
			history = append(history, provider.Message{
				Role:    "system",
				Content: fmt.Sprintf("sys-%d", rng.IntN(3)),
			})
		case 1, 2:
			history = append(history, provider.Message{
				Role:    "user",
				Content: fmt.Sprintf("u-%d", rng.IntN(5)),
			})
		case 3, 4:
			history = append(history, provider.Message{
				Role:    "assistant",
				Content: fmt.Sprintf("a-%d", rng.IntN(5)),
			})
		case 5, 6, 7:
			calls := make([]provider.ToolCall, rng.IntN(4))
			for c := range calls {
				calls[c] = provider.ToolCall{
					ID:   ids[rng.IntN(len(ids))],
					Name: "t",
					Args: []byte(`{"k":"v"}`),
				}
			}
			history = append(history, provider.Message{
				Role:      "assistant",
				Content:   fmt.Sprintf("ac-%d", rng.IntN(3)),
				ToolCalls: calls,
			})
		default:
			id := ""
			if rng.IntN(4) > 0 {
				id = ids[rng.IntN(len(ids))]
			}
			history = append(history, provider.Message{
				Role:       "tool",
				Content:    fmt.Sprintf("r-%d", rng.IntN(5)),
				ToolCallID: id,
			})
		}
	}
	return history
}

// TestHistoryInvariantProperty drives the trim policy, Truncate, and Compact
// with malformed random histories. Every output must satisfy the pairing
// invariant, and trimming must only drop whole leading turns: the assistant
// messages that survive are an exact suffix of the input's.
func TestHistoryInvariantProperty(t *testing.T) {
	rng := rand.New(rand.NewPCG(0x5EED, 0xC0DE))
	for iter := range 1000 {
		history := randomHistory(rng)
		// An unfinished tail is a legitimate live-turn state; close it so the
		// transforms are judged on settled history.
		if inFlightToolCalls(history) {
			history = append(history, provider.Message{Role: "user", Content: "tail"})
		}

		keepTurns := rng.IntN(8) // 0..7
		trimmed := trimHistory(history, keepTurns)
		assertHistoryInvariant(t, fmt.Sprintf("iter %d trim keep=%d", iter, keepTurns), trimmed)
		assertAssistantSuffix(t, fmt.Sprintf("iter %d trim", iter), history, trimmed)

		cm := NewContextManager(rng.IntN(40) + 1)
		truncated := cm.Truncate(history)
		assertHistoryInvariant(t, fmt.Sprintf("iter %d truncate", iter), truncated)
		assertAssistantSuffix(t, fmt.Sprintf("iter %d truncate", iter), history, truncated)

		compacted := cm.Compact(history, rng.IntN(3)+1)
		assertHistoryInvariant(t, fmt.Sprintf("iter %d compact", iter), compacted)

		// A second repair pass keeps the invariant and never loses assistant text.
		repaired := EnsureToolPairing(truncated)
		assertHistoryInvariant(t, fmt.Sprintf("iter %d repair twice", iter), repaired)
		assertAssistantSuffix(t, fmt.Sprintf("iter %d repair twice", iter), history, repaired)
	}
}
