package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
)

// The bus path must reproduce the v0.7.5 record stream exactly: same record
// types, same order, same field shapes, turn_done last with metrics. This is
// the additive-only schema contract defended end-to-end through the broker.
func TestHeadlessBusPathSchemaEquivalence(t *testing.T) {
	p := &headlessScriptProvider{scripts: [][]provider.StreamChunk{
		{
			{Delta: "hello "},
			{Delta: "world"},
			{ToolCallDelta: &provider.ToolCallDelta{ID: "c1", Name: "probe", ArgsDelta: `{"a":1`}},
			{ToolCallDelta: &provider.ToolCallDelta{ID: "c1", Name: "probe", ArgsDelta: `,"b":2}`}},
			{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "probe", Args: []byte(`{"a":1,"b":2}`)}}},
			{Done: true},
		},
		{
			{Delta: "fin"},
			{Done: true},
		},
	}}
	tool := &headlessFakeTool{name: "probe", output: "ok"}
	loop := newHeadlessLoop(t, p, nil, tool)

	input := strings.Join([]string{
		`{"type":"user","text":"hi"}`,
		`{"type":"exit"}`,
		"",
	}, "\n")

	var out bytes.Buffer
	if err := runHeadless(context.Background(), strings.NewReader(input), &out, loop); err != nil {
		t.Fatalf("runHeadless: %v", err)
	}

	records := decodeHeadlessRecords(t, out.String())
	// The notice is the loop guard's end-of-turn tool summary (v0.7.5).
	wantTypes := []string{"delta", "delta", "tool_delta", "tool_delta", "tool_start", "tool_result", "delta", "notice", "turn_done"}
	if len(records) != len(wantTypes) {
		t.Fatalf("got %d records, want %d: %s", len(records), len(wantTypes), out.String())
	}
	for i, want := range wantTypes {
		if records[i]["type"] != want {
			t.Fatalf("record %d: type %v, want %s (order must match v0.7.5)", i, records[i]["type"], want)
		}
	}

	// Every delta frame survives (no coalescing on the frame stream).
	if records[0]["text"] != "hello " || records[1]["text"] != "world" || records[6]["text"] != "fin" {
		t.Fatalf("delta frames lost or reordered: %s", out.String())
	}
	if records[7]["text"] != "[GoCode] 1 tool call(s)." {
		t.Fatalf("guard summary notice changed: %v", records[7])
	}
	// Both tool_delta frames survive with accumulated previews.
	if records[2]["args_so_far"] != `{"a":1` || records[3]["args_so_far"] != `{"a":1,"b":2}` {
		t.Fatalf("tool_delta frames lost or reordered: %s", out.String())
	}
	if records[2]["tool_call_id"] != "c1" || records[2]["name"] != "probe" {
		t.Fatalf("tool_delta identity fields changed: %v", records[2])
	}
	// tool_start carries raw args; tool_result keeps text/is_error unconditional.
	if records[4]["tool"] != "probe" {
		t.Fatalf("tool_start shape changed: %v", records[4])
	}
	if records[5]["tool"] != "probe" || records[5]["text"] != "ok" || records[5]["is_error"] != false {
		t.Fatalf("tool_result shape changed: %v", records[5])
	}
	// turn_done closes the turn and carries the metrics object.
	last := records[len(records)-1]
	if last["type"] != "turn_done" {
		t.Fatalf("turn_done must be the last record, got %v", last["type"])
	}
	metrics, ok := last["metrics"].(map[string]any)
	if !ok {
		t.Fatalf("turn_done lost its metrics: %v", last)
	}
	for _, key := range []string{"prompt_tokens", "completion_tokens", "tool_calls", "elapsed_ms", "retries", "provider_reported", "context_tokens", "context_budget"} {
		if _, present := metrics[key]; !present {
			t.Fatalf("metrics field %q missing: %v", key, metrics)
		}
	}
	// The bus delivered everything: no lossy or must-deliver drops.
	if loop.Bus == nil {
		t.Fatal("headless must wire the loop onto the bus")
	}
	if loop.Bus.DropCount() != 0 || loop.Bus.MustDeliverDropCount() != 0 {
		t.Fatalf("bus dropped events: lossy=%d must=%d", loop.Bus.DropCount(), loop.Bus.MustDeliverDropCount())
	}
}
