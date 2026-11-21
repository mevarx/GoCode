package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

// headless contract: the policy stays deny-by-default; auto_approve_session
// is an explicit additive opt-in to never-prompting, and the
// deny-list outranks it. The JSONL schema is unchanged — permission payloads
// ride the bus for observers, not new protocol records.

func TestHeadlessPolicyAutoApproveSessionOptIn(t *testing.T) {
	mutate := &headlessFakeTool{name: "file_write", requires: true, output: "written"}
	p := &headlessScriptProvider{scripts: [][]provider.StreamChunk{
		{{ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "file_write", Args: json.RawMessage(`{"path":"a.txt"}`)},
		}}},
		{{Delta: "done"}, {Done: true}},
	}}

	policy := headlessPolicy{AutoApproveSession: true}
	gate := newHeadlessApprovalGate(policy)
	loop := newHeadlessLoop(t, p, gate, mutate)
	// main.go wires the opt-in to the loop's session after construction.
	gate.AutoApproveSession(loop.Session.ID())

	input := strings.Join([]string{
		`{"type":"user","text":"write it"}`,
		`{"type":"exit"}`,
		"",
	}, "\n")
	var out bytes.Buffer
	if err := runHeadless(context.Background(), strings.NewReader(input), &out, loop); err != nil {
		t.Fatalf("runHeadless: %v", err)
	}
	if got := len(mutate.executedArgs()); got != 1 {
		t.Fatalf("opt-in session must grant non-allowlisted tools: executions = %d", got)
	}
	// Schema unchanged: no permission record types leak into the JSONL.
	for _, rec := range decodeHeadlessRecords(t, out.String()) {
		if strings.HasPrefix(rec["type"].(string), "permission") {
			t.Fatalf("unexpected permission record in headless schema: %v", rec)
		}
	}
}

func TestHeadlessAutoApproveSessionDenyStillWins(t *testing.T) {
	shell := &headlessFakeTool{name: "shell_exec", requires: true, output: "ran"}
	p := &headlessScriptProvider{scripts: [][]provider.StreamChunk{
		{{ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "shell_exec", Args: json.RawMessage(`{"command":"ls"}`)},
		}}},
		{{Delta: "done"}, {Done: true}},
	}}

	policy := headlessPolicy{AutoApproveSession: true, Deny: []string{"shell_exec"}}
	gate := newHeadlessApprovalGate(policy)
	loop := newHeadlessLoop(t, p, gate, shell)
	gate.AutoApproveSession(loop.Session.ID())

	input := strings.Join([]string{
		`{"type":"user","text":"run it"}`,
		`{"type":"exit"}`,
		"",
	}, "\n")
	var out bytes.Buffer
	if err := runHeadless(context.Background(), strings.NewReader(input), &out, loop); err != nil {
		t.Fatalf("runHeadless: %v", err)
	}
	if got := len(shell.executedArgs()); got != 0 {
		t.Fatalf("deny-list must outrank session auto-approve: executions = %d", got)
	}
}

// Without the opt-in the historical deny-by-default contract stands.
func TestHeadlessDefaultStillDeniesNonAllowlisted(t *testing.T) {
	mutate := &headlessFakeTool{name: "file_write", requires: true, output: "written"}
	p := &headlessScriptProvider{scripts: [][]provider.StreamChunk{
		{{ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "file_write", Args: json.RawMessage(`{"path":"a.txt"}`)},
		}}},
		{{Delta: "done"}, {Done: true}},
	}}
	loop := newHeadlessLoop(t, p, newHeadlessApprovalGate(headlessPolicy{}), mutate)
	input := strings.Join([]string{
		`{"type":"user","text":"write it"}`,
		`{"type":"exit"}`,
		"",
	}, "\n")
	var out bytes.Buffer
	if err := runHeadless(context.Background(), strings.NewReader(input), &out, loop); err != nil {
		t.Fatalf("runHeadless: %v", err)
	}
	if got := len(mutate.executedArgs()); got != 0 {
		t.Fatalf("default headless must deny: executions = %d", got)
	}
}

// Yolo in headless: dangerous calls are refused with the way out; safe calls
// run without a human.
func TestHeadlessYoloMode(t *testing.T) {
	gate := newHeadlessApprovalGate(headlessPolicy{})
	if err := gate.SetMode(tools.ModeYolo); err != nil {
		t.Fatal(err)
	}
	tool := &headlessFakeTool{name: "shell_exec", requires: true, output: "ran"}

	execute, err := gate.Prepare(context.Background(), tool, json.RawMessage(`{"command":"rm -rf /"}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Error, "refused in yolo mode") || !strings.Contains(res.Error, "use normal mode or add an allow rule") {
		t.Fatalf("yolo refusal = %q", res.Error)
	}
	if got := len(tool.executedArgs()); got != 0 {
		t.Fatalf("refused tool executed %d times", got)
	}

	execute, err = gate.Prepare(context.Background(), tool, json.RawMessage(`{"command":"ls"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(tool.executedArgs()); got != 1 {
		t.Fatalf("safe yolo call must run: executions = %d", got)
	}
}

// The additive policy fields parse; old policies without them keep working.
func TestLoadHeadlessPolicyM7Fields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.json")
	contents := `{"auto_approve":["file_read"],"deny":["shell_exec"],"auto_approve_session":true,"dangerous_tools":["mcp_deploy"]}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := loadHeadlessPolicy(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !policy.AutoApproveSession {
		t.Fatal("auto_approve_session not parsed")
	}
	if len(policy.DangerousTools) != 1 || policy.DangerousTools[0] != "mcp_deploy" {
		t.Fatalf("dangerous_tools = %v", policy.DangerousTools)
	}

	old := filepath.Join(dir, "old.json")
	if err := os.WriteFile(old, []byte(`{"auto_approve":["file_read"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy, err := loadHeadlessPolicy(old)
	if err != nil {
		t.Fatalf("legacy policy: %v", err)
	}
	if legacy.AutoApproveSession || len(legacy.DangerousTools) != 0 {
		t.Fatalf("legacy policy gained defaults: %+v", legacy)
	}
}
