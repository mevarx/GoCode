package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mevarx/GoCode/internal/pubsub"
)

// fakePermBus captures everything the gate publishes, in order.
type fakePermBus struct {
	mu     sync.Mutex
	reqs   []pubsub.PermissionRequest
	notifs []pubsub.PermissionNotification
	modes  []pubsub.PermissionModeChanged
}

func (b *fakePermBus) PublishPermissionRequest(_ context.Context, r pubsub.PermissionRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reqs = append(b.reqs, r)
}

func (b *fakePermBus) PublishPermissionNotification(_ context.Context, n pubsub.PermissionNotification) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.notifs = append(b.notifs, n)
}

func (b *fakePermBus) PublishPermissionModeChanged(_ context.Context, c pubsub.PermissionModeChanged) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.modes = append(b.modes, c)
}

func (b *fakePermBus) snapshot() ([]pubsub.PermissionRequest, []pubsub.PermissionNotification, []pubsub.PermissionModeChanged) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]pubsub.PermissionRequest(nil), b.reqs...),
		append([]pubsub.PermissionNotification(nil), b.notifs...),
		append([]pubsub.PermissionModeChanged(nil), b.modes...)
}

// waitForPending blocks until the gate has an open prompt and returns its id.
func waitForPending(t *testing.T, g *ApprovalGate) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ids := g.PendingRequestIDs(); len(ids) > 0 {
			return ids[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no pending permission request appeared")
	return ""
}

// --- decision order / mode precedence ---

// The precedence contract: deny-list beats yolo beats auto_approve beats
// keyed grants beats the prompt. Each row is decided by the FIRST matching
// tier, observable through the result text and whether a prompt happened.
func TestM7DecisionOrderPrecedence(t *testing.T) {
	dangerousArgs := json.RawMessage(`{"command":"rm -rf /"}`)
	safeArgs := json.RawMessage(`{"command":"ls"}`)

	t.Run("deny beats yolo and auto_approve and grant", func(t *testing.T) {
		g := NewApprovalGateWithPermissions([]string{"shell_exec"}, []string{"shell_exec"})
		if err := g.SetMode(ModeYolo); err != nil {
			t.Fatal(err)
		}
		prompts := 0
		g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { prompts++; return ApprovalOnce, nil }
		tool := &grantTool{name: "shell_exec", requiresApproval: true}
		res, err := g.WrapExecution(context.Background(), tool, dangerousArgs)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res.Error, "denied by permissions configuration") {
			t.Fatalf("deny-list must win, got %q", res.Error)
		}
		if tool.executions != 0 || prompts != 0 {
			t.Fatalf("denied call executed or prompted: execs=%d prompts=%d", tool.executions, prompts)
		}
	})

	t.Run("yolo refusal beats auto_approve", func(t *testing.T) {
		g := NewApprovalGateWithPermissions([]string{"shell_exec"}, nil)
		if err := g.SetMode(ModeYolo); err != nil {
			t.Fatal(err)
		}
		prompts := 0
		g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { prompts++; return ApprovalOnce, nil }
		tool := &grantTool{name: "shell_exec", requiresApproval: true}
		res, err := g.WrapExecution(context.Background(), tool, dangerousArgs)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res.Error, "refused in yolo mode") {
			t.Fatalf("yolo must refuse dangerous even when allowlisted, got %q", res.Error)
		}
		if tool.executions != 0 || prompts != 0 {
			t.Fatalf("refused call executed or prompted: execs=%d prompts=%d", tool.executions, prompts)
		}
	})

	t.Run("yolo grants safe without prompt", func(t *testing.T) {
		g := NewApprovalGate()
		if err := g.SetMode(ModeYolo); err != nil {
			t.Fatal(err)
		}
		prompts := 0
		g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { prompts++; return ApprovalDeny, nil }
		tool := &grantTool{name: "shell_exec", requiresApproval: true}
		res, err := g.WrapExecution(context.Background(), tool, safeArgs)
		if err != nil {
			t.Fatal(err)
		}
		if tool.executions != 1 || prompts != 0 {
			t.Fatalf("yolo safe call: execs=%d prompts=%d, want 1/0", tool.executions, prompts)
		}
		if res.Error != "" {
			t.Fatalf("safe yolo call errored: %q", res.Error)
		}
	})

	t.Run("auto_approve beats prompt", func(t *testing.T) {
		g := NewApprovalGateWithPermissions([]string{"mutate"}, nil)
		prompts := 0
		g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { prompts++; return ApprovalDeny, nil }
		tool := &grantTool{name: "mutate", requiresApproval: true}
		if _, err := g.WrapExecution(context.Background(), tool, safeArgs); err != nil {
			t.Fatal(err)
		}
		if tool.executions != 1 || prompts != 0 {
			t.Fatalf("allowlisted call: execs=%d prompts=%d, want 1/0", tool.executions, prompts)
		}
	})

	t.Run("grant beats prompt", func(t *testing.T) {
		g := NewApprovalGate()
		g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { return ApprovalGrantExact, nil }
		tool := &grantTool{name: "mutate", requiresApproval: true}
		if _, err := g.WrapExecution(context.Background(), tool, safeArgs); err != nil {
			t.Fatal(err)
		}
		prompts := 0
		g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { prompts++; return ApprovalDeny, nil }
		res, err := g.WrapExecution(context.Background(), tool, safeArgs)
		if err != nil {
			t.Fatal(err)
		}
		if prompts != 0 || !strings.Contains(res.Output, "(session grant)") {
			t.Fatalf("grant must bypass prompt: prompts=%d out=%q", prompts, res.Output)
		}
	})

	t.Run("no tier matches prompts", func(t *testing.T) {
		g := NewApprovalGate()
		prompts := 0
		g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { prompts++; return ApprovalDeny, nil }
		tool := &grantTool{name: "mutate", requiresApproval: true}
		res, err := g.WrapExecution(context.Background(), tool, safeArgs)
		if err != nil {
			t.Fatal(err)
		}
		if prompts != 1 || !strings.Contains(res.Output, "denied") {
			t.Fatalf("unmatched call must prompt: prompts=%d out=%q", prompts, res.Output)
		}
	})
}

// The yolo refusal names the way out verbatim and carries the classifier
// reason; it arrives as a normal tool result (no modal, no prompt).
func TestM7YoloRefusalNamesWayOut(t *testing.T) {
	g := NewApprovalGate()
	if err := g.SetMode(ModeYolo); err != nil {
		t.Fatal(err)
	}
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) {
		t.Error("yolo must not prompt")
		return ApprovalDeny, nil
	}
	tool := &grantTool{name: "shell_exec", requiresApproval: true}
	res, err := g.WrapExecution(context.Background(), tool, json.RawMessage(`{"command":"sudo rm -rf /"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"refused in yolo mode:", "use normal mode or add an allow rule"} {
		if !strings.Contains(res.Error, want) {
			t.Fatalf("refusal %q missing %q", res.Error, want)
		}
	}
	if !strings.Contains(res.Error, "sudo") && !strings.Contains(res.Error, "rm -rf") {
		t.Fatalf("refusal %q missing the classifier reason", res.Error)
	}
	if tool.executions != 0 {
		t.Fatal("refused call executed")
	}
}

// The configurable danger list makes any listed tool dangerous in both modes.
func TestM7ConfigurableDangerList(t *testing.T) {
	g := NewApprovalGate()
	g.SetDangerTools([]string{"custom_tool"})

	var seen ApprovalPrompt
	g.OnPresentPrompt = func(p ApprovalPrompt) (ApprovalChoice, error) { seen = p; return ApprovalDeny, nil }
	tool := &grantTool{name: "custom_tool", requiresApproval: true}
	if _, err := g.WrapExecution(context.Background(), tool, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if seen.Danger == "" {
		t.Fatal("listed tool must present a danger reason in normal mode")
	}

	if err := g.SetMode(ModeYolo); err != nil {
		t.Fatal(err)
	}
	res, err := g.WrapExecution(context.Background(), tool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Error, "refused in yolo mode") || !strings.Contains(res.Error, "danger list") {
		t.Fatalf("listed tool must be refused in yolo, got %q", res.Error)
	}
}

// "tool:action" allowlist entries match one action; bare "tool" matches all.
func TestM7AutoApproveToolAction(t *testing.T) {
	cases := []struct {
		allow string
		tool  string
		match bool
	}{
		{"file_write:write", "file_write", true},
		{"file_write:read", "file_write", false},
		{"file_write", "file_write", true},
		{"shell_exec:exec", "shell_exec", true},
		{"shell_exec:read", "shell_exec", false},
		{"file_patch:patch", "file_patch", true},
		{"code_search:search", "code_search", true},
	}
	for _, tc := range cases {
		g := NewApprovalGateWithPermissions([]string{tc.allow}, nil)
		prompts := 0
		g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { prompts++; return ApprovalDeny, nil }
		tool := &grantTool{name: tc.tool, requiresApproval: true}
		if _, err := g.WrapExecution(context.Background(), tool, json.RawMessage(`{"path":"a.go"}`)); err != nil {
			t.Fatal(err)
		}
		if tc.match && (prompts != 0 || tool.executions != 1) {
			t.Errorf("allow %q tool %q: want auto-grant, prompts=%d execs=%d", tc.allow, tc.tool, prompts, tool.executions)
		}
		if !tc.match && prompts != 1 {
			t.Errorf("allow %q tool %q: want prompt, prompts=%d", tc.allow, tc.tool, prompts)
		}
	}
}

// --- keyed session grants ---

// bash GrantKey is the command: approving ls does not approve sudo.
func TestM7KeyedGrantBashCommand(t *testing.T) {
	root := t.TempDir()
	g := NewApprovalGate()
	g.SetWorkspaceRoot(root)
	bus := &fakePermBus{}
	g.Bus = bus

	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { return ApprovalGrantExact, nil }
	tool := &grantTool{name: "shell_exec", requiresApproval: true}
	lsArgs := json.RawMessage(`{"command":"ls -la"}`)
	if _, err := g.WrapExecution(context.Background(), tool, lsArgs); err != nil {
		t.Fatal(err)
	}

	keys := g.SessionGrantKeys()
	if len(keys) != 1 {
		t.Fatalf("grants = %d, want 1", len(keys))
	}
	if keys[0].GrantKey != "ls -la" {
		t.Fatalf("bash grant key = %q, want the command", keys[0].GrantKey)
	}
	if keys[0].Path != root {
		t.Fatalf("bash grant path = %q, want workspace %q", keys[0].Path, root)
	}
	if keys[0].ToolName != "shell_exec" || keys[0].Action != "exec" {
		t.Fatalf("grant key tool/action = %q/%q", keys[0].ToolName, keys[0].Action)
	}

	// Same command: auto-granted, reported, and a Granted notification fires.
	prompts := 0
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { prompts++; return ApprovalDeny, nil }
	res, err := g.WrapExecution(context.Background(), tool, lsArgs)
	if err != nil {
		t.Fatal(err)
	}
	if prompts != 0 || !strings.Contains(res.Output, "(session grant)") {
		t.Fatalf("same command must hit the grant: prompts=%d out=%q", prompts, res.Output)
	}
	_, notifs, _ := bus.snapshot()
	var granted int
	for _, n := range notifs {
		if n.Granted {
			granted++
		}
	}
	if granted == 0 {
		t.Fatal("grant hit must publish a Granted notification")
	}

	// Different command (sudo): the grant does NOT cover it.
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { prompts++; return ApprovalDeny, nil }
	res, err = g.WrapExecution(context.Background(), tool, json.RawMessage(`{"command":"sudo ls -la"}`))
	if err != nil {
		t.Fatal(err)
	}
	if prompts != 1 {
		t.Fatalf("sudo must re-prompt, prompts=%d", prompts)
	}
	if !strings.Contains(res.Output, "denied") || tool.executions != 2 {
		t.Fatalf("sudo call must be denied: out=%q execs=%d", res.Output, tool.executions)
	}
}

// Non-shell tools keep the canonical exact-args grant class, and the path
// component is normalized to a DIRECTORY (file → dir, "." → workspace).
func TestM7KeyedGrantExactArgsAndDirScope(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	g := NewApprovalGate()
	g.SetWorkspaceRoot(root)
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { return ApprovalGrantExact, nil }
	tool := &grantTool{name: "file_write", requiresApproval: true}

	if _, err := g.WrapExecution(context.Background(), tool, json.RawMessage(`{"path":"sub/file.go","content":"x"}`)); err != nil {
		t.Fatal(err)
	}
	keys := g.SessionGrantKeys()
	if len(keys) != 1 || keys[0].Path != sub {
		t.Fatalf("grant path = %+v, want dir %q", keys, sub)
	}
	if keys[0].Action != "write" {
		t.Fatalf("file_write action = %q, want write", keys[0].Action)
	}

	// Canonical key order differs but the args are equivalent → grant hit.
	prompts := 0
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { prompts++; return ApprovalDeny, nil }
	res, err := g.WrapExecution(context.Background(), tool, json.RawMessage(`{"content":"x","path":"sub/file.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	if prompts != 0 || !strings.Contains(res.Output, "(session grant)") {
		t.Fatalf("canonical args must hit: prompts=%d out=%q", prompts, res.Output)
	}

	// Same directory, different content → exact-args class re-prompts.
	if _, err := g.WrapExecution(context.Background(), tool, json.RawMessage(`{"path":"sub/file.go","content":"y"}`)); err != nil {
		t.Fatal(err)
	}
	if prompts != 1 {
		t.Fatalf("changed args must re-prompt: prompts=%d", prompts)
	}

	// "." normalizes to the workspace directory in the key.
	g2 := NewApprovalGate()
	g2.SetWorkspaceRoot(root)
	g2.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { return ApprovalGrantExact, nil }
	if _, err := g2.WrapExecution(context.Background(), tool, json.RawMessage(`{"path":"."}`)); err != nil {
		t.Fatal(err)
	}
	if keys := g2.SessionGrantKeys(); len(keys) != 1 || keys[0].Path != root {
		t.Fatalf("'.' grant path = %+v, want workspace %q", keys, root)
	}
}

// Grants are session-scoped: a different session id re-prompts.
func TestM7KeyedGrantSessionScoped(t *testing.T) {
	g := NewApprovalGate()
	g.SetSessionID("session-a")
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { return ApprovalGrantExact, nil }
	tool := &grantTool{name: "mutate", requiresApproval: true}
	args := json.RawMessage(`{"x":1}`)
	if _, err := g.WrapExecution(context.Background(), tool, args); err != nil {
		t.Fatal(err)
	}
	if keys := g.SessionGrantKeys(); len(keys) != 1 || keys[0].SessionID != "session-a" {
		t.Fatalf("grant session = %+v", keys)
	}

	g.SetSessionID("session-b")
	prompts := 0
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { prompts++; return ApprovalDeny, nil }
	if _, err := g.WrapExecution(context.Background(), tool, args); err != nil {
		t.Fatal(err)
	}
	if prompts != 1 {
		t.Fatalf("another session must re-prompt: prompts=%d", prompts)
	}
}

// --- first-answer-wins + server-stored params ---

// Double resolve: the first answer wins, the second is a no-op.
func TestM7FirstAnswerWinsDoubleResolve(t *testing.T) {
	g := NewApprovalGate()
	bus := &fakePermBus{}
	g.Bus = bus
	release := make(chan struct{})
	entered := make(chan struct{})
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) {
		close(entered)
		<-release
		return ApprovalDeny, nil // the late hook answer must NOT win
	}
	tool := &grantTool{name: "mutate", requiresApproval: true}
	done := make(chan Result, 1)
	go func() {
		res, _ := g.WrapExecution(context.Background(), tool, json.RawMessage(`{"x":1}`))
		done <- res
	}()
	<-entered
	id := waitForPending(t, g)

	if !g.Resolve(id, ApprovalOnce) {
		t.Fatal("first resolve must win")
	}
	if g.Resolve(id, ApprovalDeny) {
		t.Fatal("second resolve must be a no-op")
	}
	close(release) // late hook answer arrives after resolution

	select {
	case res := <-done:
		if tool.executions != 1 {
			t.Fatalf("winning Once must execute: execs=%d out=%q", tool.executions, res.Output)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request hung after resolution")
	}
	if ids := g.PendingRequestIDs(); len(ids) != 0 {
		t.Fatalf("pending leaked: %v", ids)
	}
	// Exactly one terminal notification, Granted (the first answer).
	_, notifs, _ := bus.snapshot()
	terminal := 0
	for _, n := range notifs {
		if !n.Open() {
			terminal++
			if !n.Granted {
				t.Fatalf("terminal notification = %+v, want Granted", n)
			}
		}
	}
	if terminal != 1 {
		t.Fatalf("terminal notifications = %d, want exactly 1", terminal)
	}
}

// Concurrent resolvers: exactly one wins the race.
func TestM7FirstAnswerWinsRace(t *testing.T) {
	g := NewApprovalGate()
	release := make(chan struct{})
	entered := make(chan struct{})
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) {
		close(entered)
		<-release
		return ApprovalDeny, nil
	}
	tool := &grantTool{name: "mutate", requiresApproval: true}
	done := make(chan struct{})
	go func() {
		_, _ = g.WrapExecution(context.Background(), tool, json.RawMessage(`{}`))
		close(done)
	}()
	<-entered
	id := waitForPending(t, g)

	const n = 8
	var wg sync.WaitGroup
	wins := make(chan bool, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			wins <- g.Resolve(id, ApprovalChoice(i%2)) // Once or Deny
		}(i)
	}
	close(start)
	wg.Wait()
	close(release)
	<-done

	count := 0
	for i := 0; i < n; i++ {
		if <-wins {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("winners = %d, want exactly 1", count)
	}
}

// A resolve after cancellation is a no-op; the cancelled denial stands.
func TestM7LateResolveAfterCancel(t *testing.T) {
	g := NewApprovalGate()
	bus := &fakePermBus{}
	g.Bus = bus
	release := make(chan struct{})
	entered := make(chan struct{})
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) {
		close(entered)
		<-release
		return ApprovalOnce, nil
	}
	tool := &grantTool{name: "mutate", requiresApproval: true}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result, 1)
	go func() {
		res, _ := g.WrapExecution(ctx, tool, json.RawMessage(`{}`))
		done <- res
	}()
	<-entered
	id := waitForPending(t, g)
	cancel()

	select {
	case res := <-done:
		if !strings.Contains(res.Output, "cancelled") || tool.executions != 0 {
			t.Fatalf("cancel must deny without executing: out=%q execs=%d", res.Output, tool.executions)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled request hung")
	}
	if g.Resolve(id, ApprovalOnce) {
		t.Fatal("late resolve after cancel must be a no-op")
	}
	close(release)
	_, notifs, _ := bus.snapshot()
	var cancelled int
	for _, n := range notifs {
		if n.Cancelled {
			cancelled++
		}
	}
	if cancelled != 1 {
		t.Fatalf("Cancelled notifications = %d, want 1", cancelled)
	}
}

// Resolution uses the SERVER-stored params: the grant recorded by an external
// resolve covers exactly the pending args, and a responder cannot widen it
// (Resolve carries only id + choice; different args still prompt).
func TestM7ResolveUsesServerStoredParams(t *testing.T) {
	g := NewApprovalGate()
	release := make(chan struct{})
	entered := make(chan struct{})
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) {
		close(entered)
		<-release
		return ApprovalDeny, nil
	}
	tool := &grantTool{name: "shell_exec", requiresApproval: true}
	serverArgs := json.RawMessage(`{"command":"ls"}`)
	done := make(chan struct{})
	go func() {
		_, _ = g.WrapExecution(context.Background(), tool, serverArgs)
		close(done)
	}()
	<-entered
	id := waitForPending(t, g)
	if !g.Resolve(id, ApprovalGrantExact) {
		t.Fatal("resolve must win")
	}
	close(release)
	<-done

	keys := g.SessionGrantKeys()
	if len(keys) != 1 || keys[0].GrantKey != "ls" {
		t.Fatalf("grant must use server-stored command, got %+v", keys)
	}
	// A different command is NOT covered — the grant was not widened.
	prompts := 0
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { prompts++; return ApprovalDeny, nil }
	if _, err := g.WrapExecution(context.Background(), tool, json.RawMessage(`{"command":"sudo ls"}`)); err != nil {
		t.Fatal(err)
	}
	if prompts != 1 {
		t.Fatalf("different args must prompt: prompts=%d", prompts)
	}
	// Resolve rejects the non-terminal edit choice and unknown ids.
	if g.Resolve("no-such-id", ApprovalOnce) {
		t.Fatal("unknown id must not resolve")
	}
	if g.Resolve(id, ApprovalEdit) {
		t.Fatal("edit is not a terminal resolve answer")
	}
}

// --- scope serialization ---

// Default global scope: two concurrent requests queue; the second prompt is
// not shown until the first resolves.
func TestM7GlobalScopeSerializesPrompts(t *testing.T) {
	g := NewApprovalGate()
	enter1, enter2 := make(chan struct{}), make(chan struct{})
	release1, release2 := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	order := []string{}
	g.OnPresentPrompt = func(p ApprovalPrompt) (ApprovalChoice, error) {
		mu.Lock()
		order = append(order, p.ToolName)
		first := len(order) == 1
		mu.Unlock()
		if first {
			close(enter1)
			<-release1
		} else {
			close(enter2)
			<-release2
		}
		return ApprovalOnce, nil
	}
	toolA := &grantTool{name: "tool_a", requiresApproval: true}
	toolB := &grantTool{name: "tool_b", requiresApproval: true}
	doneA, doneB := make(chan struct{}), make(chan struct{})
	go func() { _, _ = g.WrapExecution(context.Background(), toolA, json.RawMessage(`{}`)); close(doneA) }()
	<-enter1
	go func() { _, _ = g.WrapExecution(context.Background(), toolB, json.RawMessage(`{}`)); close(doneB) }()

	select {
	case <-enter2:
		t.Fatal("second prompt shown while the first is open (global scope violated)")
	case <-time.After(100 * time.Millisecond):
	}
	close(release1)
	select {
	case <-enter2:
	case <-time.After(3 * time.Second):
		t.Fatal("second prompt never shown after the first resolved")
	}
	close(release2)
	<-doneA
	<-doneB
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(order, ",") != "tool_a,tool_b" {
		t.Fatalf("prompt order = %v", order)
	}
}

// A grant recorded by the first prompt auto-grants an identical queued call
// (the re-check under the scope lock), so no second dialog appears.
func TestM7QueuedIdenticalCallHitsGrantAfterFirst(t *testing.T) {
	g := NewApprovalGate()
	entered := make(chan struct{})
	release := make(chan struct{})
	prompts := 0
	var mu sync.Mutex
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) {
		mu.Lock()
		prompts++
		n := prompts
		mu.Unlock()
		if n == 1 {
			close(entered)
			<-release
			return ApprovalGrantExact, nil
		}
		return ApprovalDeny, nil
	}
	// Separate instances (grantTool counters are single-goroutine fakes);
	// the grant key depends on tool NAME + args, not the instance.
	toolA := &grantTool{name: "mutate", requiresApproval: true}
	toolB := &grantTool{name: "mutate", requiresApproval: true}
	args := json.RawMessage(`{"x":1}`)
	doneA, doneB := make(chan Result, 1), make(chan Result, 1)
	go func() { r, _ := g.WrapExecution(context.Background(), toolA, args); doneA <- r }()
	<-entered
	go func() { r, _ := g.WrapExecution(context.Background(), toolB, args); doneB <- r }()
	time.Sleep(50 * time.Millisecond) // let B queue behind A
	close(release)
	<-doneA
	select {
	case r := <-doneB:
		if !strings.Contains(r.Output, "(session grant)") {
			t.Fatalf("queued identical call must hit the fresh grant: %q", r.Output)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued call hung")
	}
	if toolA.executions != 1 || toolB.executions != 1 {
		t.Fatalf("executions = (%d, %d), want (1, 1)", toolA.executions, toolB.executions)
	}
	mu.Lock()
	defer mu.Unlock()
	if prompts != 1 {
		t.Fatalf("prompts = %d, want 1 (second call auto-granted)", prompts)
	}
}

// SessionScope hook: different scopes prompt concurrently.
func TestM7SessionScopeHookAllowsParallelScopes(t *testing.T) {
	type scopeKey struct{}
	g := NewApprovalGate()
	g.SessionScope = func(ctx context.Context, _ string) string {
		if v, ok := ctx.Value(scopeKey{}).(string); ok {
			return v
		}
		return ""
	}
	enterA, enterB := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	g.OnPresentPrompt = func(p ApprovalPrompt) (ApprovalChoice, error) {
		if p.ToolName == "tool_a" {
			close(enterA)
		} else {
			close(enterB)
		}
		<-release
		return ApprovalOnce, nil
	}
	ctxA := context.WithValue(context.Background(), scopeKey{}, "scope-a")
	ctxB := context.WithValue(context.Background(), scopeKey{}, "scope-b")
	doneA, doneB := make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = g.WrapExecution(ctxA, &grantTool{name: "tool_a", requiresApproval: true}, json.RawMessage(`{}`))
		close(doneA)
	}()
	go func() {
		_, _ = g.WrapExecution(ctxB, &grantTool{name: "tool_b", requiresApproval: true}, json.RawMessage(`{}`))
		close(doneB)
	}()
	select {
	case <-enterA:
	case <-time.After(3 * time.Second):
		t.Fatal("scope A never prompted")
	}
	select {
	case <-enterB:
	case <-time.After(3 * time.Second):
		t.Fatal("scope B waited on scope A (session scopes must not serialize)")
	}
	close(release)
	<-doneA
	<-doneB
}

// Cancelling while queued for the scope denies without a prompt and without
// hanging; the open prompt ahead is unaffected.
func TestM7CancelWhileQueuedForScope(t *testing.T) {
	g := NewApprovalGate()
	bus := &fakePermBus{}
	g.Bus = bus
	entered := make(chan struct{})
	release := make(chan struct{})
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) {
		close(entered)
		<-release
		return ApprovalOnce, nil
	}
	doneA := make(chan struct{})
	go func() {
		_, _ = g.WrapExecution(context.Background(), &grantTool{name: "tool_a", requiresApproval: true}, json.RawMessage(`{}`))
		close(doneA)
	}()
	<-entered

	ctxB, cancelB := context.WithCancel(context.Background())
	doneB := make(chan Result, 1)
	go func() {
		r, _ := g.WrapExecution(ctxB, &grantTool{name: "tool_b", requiresApproval: true}, json.RawMessage(`{}`))
		doneB <- r
	}()
	time.Sleep(50 * time.Millisecond) // let B queue
	cancelB()
	select {
	case r := <-doneB:
		if !strings.Contains(r.Output, "cancelled") {
			t.Fatalf("queued cancel must deny: %q", r.Output)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued request hung on cancellation")
	}
	close(release)
	<-doneA
	_, notifs, _ := bus.snapshot()
	var cancelled int
	for _, n := range notifs {
		if n.Cancelled {
			cancelled++
		}
	}
	if cancelled != 1 {
		t.Fatalf("Cancelled notifications = %d, want 1", cancelled)
	}
}

// --- bus payloads ---

// The full lifecycle rides the bus with the fields: requested (all
// false) → request{id,session,tool_call_id,tool,args,danger,path,grant_key} →
// granted/denied/cancelled.
func TestM7BusPayloadLifecycle(t *testing.T) {
	root := t.TempDir()
	g := NewApprovalGate()
	g.SetWorkspaceRoot(root)
	g.SetSessionID("sess-1")
	bus := &fakePermBus{}
	g.Bus = bus
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { return ApprovalOnce, nil }
	tool := &grantTool{name: "shell_exec", requiresApproval: true}
	args := json.RawMessage(`{"command":"rm -rf /tmp/x"}`)
	if _, err := g.PrepareWithID(context.Background(), tool, args, "call-42"); err != nil {
		t.Fatal(err)
	}

	reqs, notifs, _ := bus.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	r := reqs[0]
	if r.ID == "" || len(r.ID) != 36 || strings.Count(r.ID, "-") != 4 {
		t.Fatalf("id %q is not a UUID-shaped hex id", r.ID)
	}
	if r.SessionID != "sess-1" || r.ToolCallID != "call-42" || r.Tool != "shell_exec" {
		t.Fatalf("payload identity = %+v", r)
	}
	if string(r.Args) != string(args) {
		t.Fatalf("payload args = %s", r.Args)
	}
	if !r.Danger || r.DangerReason == "" {
		t.Fatalf("dangerous command must carry danger + reason: %+v", r)
	}
	if r.Path != root {
		t.Fatalf("payload path = %q, want workspace dir %q", r.Path, root)
	}
	if r.GrantKey != "rm -rf /tmp/x" {
		t.Fatalf("payload grant_key = %q, want the command", r.GrantKey)
	}
	if len(notifs) != 2 {
		t.Fatalf("notifications = %d, want requested+granted", len(notifs))
	}
	if !notifs[0].Open() || notifs[0].ID != r.ID {
		t.Fatalf("first notification must be the open request: %+v", notifs[0])
	}
	if !notifs[1].Granted || notifs[1].ID != r.ID {
		t.Fatalf("second notification must be Granted for the same id: %+v", notifs[1])
	}

	// Denial lifecycle.
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { return ApprovalDeny, nil }
	if _, err := g.Prepare(context.Background(), tool, json.RawMessage(`{"command":"ls"}`)); err != nil {
		t.Fatal(err)
	}
	_, notifs, _ = bus.snapshot()
	last := notifs[len(notifs)-1]
	if !last.Denied {
		t.Fatalf("denial notification = %+v", last)
	}
}

// Mode changes publish permission_mode_changed; unknown modes are rejected.
func TestM7SetModePublishesModeChanged(t *testing.T) {
	g := NewApprovalGate()
	bus := &fakePermBus{}
	g.Bus = bus
	if err := g.SetMode(ModeYolo); err != nil {
		t.Fatal(err)
	}
	if g.Mode() != ModeYolo {
		t.Fatalf("mode = %q", g.Mode())
	}
	_, _, modes := bus.snapshot()
	if len(modes) != 1 || modes[0].Mode != "yolo" || modes[0].Previous != "normal" {
		t.Fatalf("mode change payload = %+v", modes)
	}
	// Same mode again: no duplicate event.
	if err := g.SetMode(ModeYolo); err != nil {
		t.Fatal(err)
	}
	if _, _, modes = bus.snapshot(); len(modes) != 1 {
		t.Fatalf("no-op mode change published: %+v", modes)
	}
	if err := g.SetMode(PermissionMode("sysadmin")); err == nil {
		t.Fatal("sysadmin must be rejected (omitted mode)")
	}
	if err := g.SetMode(PermissionMode("bogus")); err == nil {
		t.Fatal("unknown mode must be rejected")
	}
	if g.Mode() != ModeYolo {
		t.Fatal("rejected mode change altered the mode")
	}
}

// The danger reason reaches the presentation hook (the modal renders it).
func TestM7DangerWiredToPrompt(t *testing.T) {
	g := NewApprovalGate()
	var seen ApprovalPrompt
	g.OnPresentPrompt = func(p ApprovalPrompt) (ApprovalChoice, error) { seen = p; return ApprovalDeny, nil }
	tool := &grantTool{name: "shell_exec", requiresApproval: true}
	if _, err := g.WrapExecution(context.Background(), tool, json.RawMessage(`{"command":"sudo ls"}`)); err != nil {
		t.Fatal(err)
	}
	if seen.Danger == "" || !strings.Contains(seen.Danger, "sudo") {
		t.Fatalf("prompt danger = %q, want the sudo reason", seen.Danger)
	}
	if seen.ID == "" {
		t.Fatal("prompt must carry the request id")
	}
	// Safe call: no danger.
	if _, err := g.WrapExecution(context.Background(), tool, json.RawMessage(`{"command":"ls"}`)); err != nil {
		t.Fatal(err)
	}
	if seen.Danger != "" {
		t.Fatalf("safe call danger = %q, want empty", seen.Danger)
	}
}

// --- session auto-approve + inheritance ---

func TestM7AutoApproveSessionAndInheritance(t *testing.T) {
	g := NewApprovalGate()
	bus := &fakePermBus{}
	g.Bus = bus
	g.SetSessionID("child")
	g.AutoApproveSession("parent")
	g.InheritAutoApproval("parent", "child")
	g.InheritAutoApproval("stranger", "other")

	prompts := 0
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { prompts++; return ApprovalDeny, nil }
	tool := &grantTool{name: "mutate", requiresApproval: true}
	if _, err := g.WrapExecution(context.Background(), tool, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if tool.executions != 1 || prompts != 0 {
		t.Fatalf("inherited auto-approve must grant without prompt: execs=%d prompts=%d", tool.executions, prompts)
	}
	_, notifs, _ := bus.snapshot()
	if len(notifs) != 1 || !notifs[0].Granted {
		t.Fatalf("session auto-approve must publish Granted: %+v", notifs)
	}

	// A session without inheritance still prompts.
	g.SetSessionID("other")
	if _, err := g.WrapExecution(context.Background(), tool, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if prompts != 1 {
		t.Fatalf("unmarked session must prompt: prompts=%d", prompts)
	}
}

// --- async observer tap ---

// A slow observer never delays the tool: PromptShown/PromptResolved run off
// the requesting goroutine.
func TestM7PromptObserverAsync(t *testing.T) {
	g := NewApprovalGate()
	shownEntered := make(chan struct{})
	release := make(chan struct{})
	resolved := make(chan PromptOutcome, 2)
	var shownOnce sync.Once
	g.Observer = &blockingObserver{
		shown: func(pubsub.PermissionRequest) {
			shownOnce.Do(func() { close(shownEntered) })
			<-release // block until the test says so
		},
		resolved: func(_ pubsub.PermissionRequest, o PromptOutcome) { resolved <- o },
	}
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { return ApprovalOnce, nil }
	tool := &grantTool{name: "mutate", requiresApproval: true}

	start := time.Now()
	res, err := g.WrapExecution(context.Background(), tool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("tool waited on the observer: %v", elapsed)
	}
	if tool.executions != 1 || res.Error != "" {
		t.Fatalf("tool must run despite the blocked observer: execs=%d res=%+v", tool.executions, res)
	}
	select {
	case <-shownEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("PromptShown never fired")
	}
	close(release)
	select {
	case o := <-resolved:
		if o != PromptGranted {
			t.Fatalf("outcome = %v, want granted", o)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("PromptResolved never fired")
	}
}

type blockingObserver struct {
	shown    func(pubsub.PermissionRequest)
	resolved func(pubsub.PermissionRequest, PromptOutcome)
}

func (o *blockingObserver) PromptShown(r pubsub.PermissionRequest) { o.shown(r) }
func (o *blockingObserver) PromptResolved(r pubsub.PermissionRequest, out PromptOutcome) {
	o.resolved(r, out)
}

// A panicking observer is contained: the tool still runs.
func TestM7PromptObserverPanicContained(t *testing.T) {
	g := NewApprovalGate()
	g.Observer = &blockingObserver{
		shown:    func(pubsub.PermissionRequest) { panic("observer boom") },
		resolved: func(pubsub.PermissionRequest, PromptOutcome) { panic("observer boom") },
	}
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) { return ApprovalOnce, nil }
	tool := &grantTool{name: "mutate", requiresApproval: true}
	if _, err := g.WrapExecution(context.Background(), tool, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if tool.executions != 1 {
		t.Fatal("panicking observer killed the tool")
	}
}

// --- edit pass keeps one request id and server-stored edited args ---

func TestM7EditKeepsRequestIDAndServerArgs(t *testing.T) {
	g := NewApprovalGate()
	bus := &fakePermBus{}
	g.Bus = bus
	var ids []string
	calls := 0
	g.OnPresentPrompt = func(p ApprovalPrompt) (ApprovalChoice, error) {
		ids = append(ids, p.ID)
		calls++
		if calls == 1 {
			return ApprovalEdit, nil
		}
		return ApprovalGrantExact, nil
	}
	g.EditArgs = func(string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"command":"echo edited"}`), nil
	}
	tool := &grantTool{name: "shell_exec", requiresApproval: true}
	if _, err := g.WrapExecution(context.Background(), tool, json.RawMessage(`{"command":"echo original"}`)); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("edit must re-present the SAME request id: %v", ids)
	}
	if string(tool.lastArgs) != `{"command":"echo edited"}` {
		t.Fatalf("executed args = %s, want the edited args", tool.lastArgs)
	}
	keys := g.SessionGrantKeys()
	if len(keys) != 1 || keys[0].GrantKey != "echo edited" {
		t.Fatalf("grant must key the EDITED server-stored args: %+v", keys)
	}
	reqs, notifs, _ := bus.snapshot()
	if len(reqs) != 2 || reqs[0].ID != reqs[1].ID {
		t.Fatalf("edit must re-publish the same id: %+v", reqs)
	}
	if string(reqs[1].Args) != `{"command":"echo edited"}` {
		t.Fatalf("re-published args = %s", reqs[1].Args)
	}
	// One requested + one granted notification for the whole decision.
	if len(notifs) != 2 || !notifs[0].Open() || !notifs[1].Granted {
		t.Fatalf("edit lifecycle notifications = %+v", notifs)
	}
}

// Denied prompts and hook errors close the lifecycle exactly once.
func TestM7HookErrorClosesLifecycle(t *testing.T) {
	g := NewApprovalGate()
	bus := &fakePermBus{}
	g.Bus = bus
	g.OnPresentPrompt = func(ApprovalPrompt) (ApprovalChoice, error) {
		return ApprovalDeny, fmt.Errorf("ui exploded")
	}
	tool := &grantTool{name: "mutate", requiresApproval: true}
	res, err := g.WrapExecution(context.Background(), tool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Error, "approval error") || tool.executions != 0 {
		t.Fatalf("hook error must deny: %q execs=%d", res.Error, tool.executions)
	}
	_, notifs, _ := bus.snapshot()
	if len(notifs) != 2 || !notifs[1].Denied || notifs[1].Reason == "" {
		t.Fatalf("hook error lifecycle = %+v", notifs)
	}
	if ids := g.PendingRequestIDs(); len(ids) != 0 {
		t.Fatalf("pending leaked after hook error: %v", ids)
	}
}
