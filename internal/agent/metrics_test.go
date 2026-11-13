package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

// metricsProvider answers with a delta plus one tool call, then finishes.
// Shaped this way so one RunTurn produces both provider and tool activity.
type metricsProvider struct {
	mu    sync.Mutex
	calls int
	tool  provider.ToolCall
}

func (p *metricsProvider) Name() string { return "metrics-fake" }

func (p *metricsProvider) Models(context.Context) ([]string, error) {
	return []string{"m"}, nil
}

func (p *metricsProvider) Stream(context.Context, string, []provider.Message, []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	toolCall := p.tool
	p.mu.Unlock()

	ch := make(chan provider.StreamChunk, 4)
	go func() {
		defer close(ch)
		if call == 1 {
			ch <- provider.StreamChunk{Delta: "working"}
			ch <- provider.StreamChunk{ToolCalls: []provider.ToolCall{toolCall}}
			return
		}
		ch <- provider.StreamChunk{Delta: "done"}
		ch <- provider.StreamChunk{Done: true, FinishReason: "stop"}
	}()
	return ch, nil
}

// metricsTool requires approval so a user decision is observable.
type metricsTool struct{}

func (metricsTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{Name: "metrics_tool", Parameters: json.RawMessage(`{}`)}
}

func (metricsTool) Execute(context.Context, json.RawMessage) (tools.Result, error) {
	time.Sleep(time.Millisecond)
	return tools.Result{Output: "ok"}, nil
}

func (metricsTool) RequiresApproval() bool { return true }

// captureSlogJSON swaps the default logger for a JSON handler writing to buf
// and restores it when the test ends.
func captureSlogJSON(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// turnMetricRecords returns every parsed "turn.metrics" line.
func turnMetricRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %v\n%s", err, line)
		}
		if rec["msg"] == "turn.metrics" {
			out = append(out, rec)
		}
	}
	return out
}

func metricNumber(t *testing.T, rec map[string]any, key string) float64 {
	t.Helper()
	v, ok := rec[key].(float64)
	if !ok {
		t.Fatalf("metric %q missing or not a number: %#v", key, rec[key])
	}
	return v
}

func TestTurnMetricsLoggedOncePerTurn(t *testing.T) {
	buf := captureSlogJSON(t)

	const secret = "SUPER_SECRET"
	const privatePath = `C:\Users\someone\private.txt`
	args, marshalErr := json.Marshal(map[string]string{"secret": secret, "path": privatePath})
	if marshalErr != nil {
		t.Fatalf("marshal args: %v", marshalErr)
	}

	prov := &metricsProvider{tool: provider.ToolCall{ID: "c1", Name: "metrics_tool", Args: args}}

	toolReg := tools.NewRegistry()
	if err := toolReg.Register(metricsTool{}); err != nil {
		t.Fatalf("register tool: %v", err)
	}

	gate := tools.NewApprovalGate()
	gate.OnPresent = func(string, json.RawMessage, string) (bool, error) { return true, nil }
	var hookDecisions int
	gate.OnDecision = func(string, bool, bool) { hookDecisions++ }

	reg := provider.NewRegistry()
	reg.Register(prov)
	engine := NewAgentLoop(reg, NewSession("m"), toolReg, gate)

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}

	records := turnMetricRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("expected exactly one turn.metrics line, got %d", len(records))
	}
	rec := records[0]

	if rec["provider"] != "metrics-fake" {
		t.Errorf("provider = %v, want metrics-fake", rec["provider"])
	}
	if rec["model"] != "m" {
		t.Errorf("model = %v, want m", rec["model"])
	}
	if got := metricNumber(t, rec, "tool_call_count"); got != 1 {
		t.Errorf("tool_call_count = %v, want 1", got)
	}
	if got := metricNumber(t, rec, "approvals_approved"); got != 1 {
		t.Errorf("approvals_approved = %v, want 1 (user-approved call)", got)
	}
	if got := metricNumber(t, rec, "approvals_auto"); got != 0 {
		t.Errorf("approvals_auto = %v, want 0", got)
	}
	if got := metricNumber(t, rec, "approvals_denied"); got != 0 {
		t.Errorf("approvals_denied = %v, want 0", got)
	}
	if got := metricNumber(t, rec, "duration_ns"); got <= 0 {
		t.Errorf("duration_ns = %v, want > 0", got)
	}
	if got := metricNumber(t, rec, "prompt_tokens_est"); got < 1 {
		t.Errorf("prompt_tokens_est = %v, want >= 1", got)
	}
	if got := metricNumber(t, rec, "completion_tokens_est"); got < 1 {
		t.Errorf("completion_tokens_est = %v, want >= 1", got)
	}
	if got := metricNumber(t, rec, "completion_chars"); got < 1 {
		t.Errorf("completion_chars = %v, want >= 1", got)
	}
	if rec["truncation"] != false {
		t.Errorf("truncation = %v, want false", rec["truncation"])
	}

	latency, ok := rec["tool_latency_ms"].(map[string]any)
	if !ok {
		t.Fatalf("tool_latency_ms missing or not an object: %#v", rec["tool_latency_ms"])
	}
	if _, ok := latency["metrics_tool"]; !ok {
		t.Errorf("tool_latency_ms missing metrics_tool: %#v", latency)
	}

	// The pre-existing observer keeps working alongside the metrics hook.
	if hookDecisions != 1 {
		t.Errorf("caller's OnDecision saw %d decisions, want 1", hookDecisions)
	}

	// No payload may leak: neither tool arguments nor file paths.
	logged := buf.String()
	if strings.Contains(logged, secret) {
		t.Errorf("metrics log leaked tool argument content %q", secret)
	}
	if strings.Contains(logged, "private.txt") {
		t.Error("metrics log leaked a file path")
	}
	if strings.Contains(logged, "working") || strings.Contains(logged, "done") {
		t.Error("metrics log leaked message content")
	}
}

func TestTurnMetricsMarksTruncation(t *testing.T) {
	buf := captureSlogJSON(t)

	prov := &scriptedProvider{chunks: []provider.StreamChunk{
		{Delta: "partial answer"},
		{Done: true, FinishReason: "length"},
	}}
	sess := NewSession("m")
	engine := engineWith(prov, sess)

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}

	records := turnMetricRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("expected exactly one turn.metrics line, got %d", len(records))
	}
	rec := records[0]
	if rec["truncation"] != true {
		t.Errorf("truncation = %v, want true for finish_reason length", rec["truncation"])
	}
	if got := metricNumber(t, rec, "truncations"); got != 1 {
		t.Errorf("truncations = %v, want 1", got)
	}
	if rec["tool_call_count"] != float64(0) {
		t.Errorf("tool_call_count = %v, want 0", rec["tool_call_count"])
	}
}
