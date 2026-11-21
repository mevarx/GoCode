package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/pubsub"
	"github.com/mevarx/GoCode/internal/tools"
)

// The permission kinds map onto the must-deliver permission lanes, unkeyed
// (every prompt and transition is distinct; coalescing would drop one).
func TestBusEventForPermissionKinds(t *testing.T) {
	req := pubsub.PermissionRequest{ID: "p1", Tool: "shell_exec"}
	notif := pubsub.PermissionNotification{ID: "p1", Granted: true}
	mode := pubsub.PermissionModeChanged{Mode: "yolo", Previous: "normal"}
	cases := []struct {
		ev       LoopEvent
		wantType string
	}{
		{LoopEvent{Kind: EventPermissionRequest, PermissionRequest: &req}, pubsub.TypePermissionRequest},
		{LoopEvent{Kind: EventPermissionNotification, PermissionNotification: &notif}, pubsub.TypePermissionNotification},
		{LoopEvent{Kind: EventPermissionModeChanged, PermissionModeChanged: &mode}, pubsub.TypePermissionModeChanged},
	}
	for _, tc := range cases {
		got := busEventFor(tc.ev)
		if got.Type != tc.wantType || !got.MustDeliver || got.Key != "" {
			t.Errorf("kind %v: got {Type:%q Must:%v Key:%q}, want {%q true \"\"}", tc.ev.Kind, got.Type, got.MustDeliver, got.Key, tc.wantType)
		}
	}
}

// permRecorder collects bus events until the stop kind arrives.
type permRecorder struct {
	events []pubsub.Event[LoopEvent]
	done   chan struct{}
}

func recordPermBus(t *testing.T, b *pubsub.Broker[LoopEvent], stop LoopEventKind) *permRecorder {
	t.Helper()
	ch, unsub := b.Subscribe()
	t.Cleanup(unsub)
	r := &permRecorder{done: make(chan struct{})}
	go func() {
		defer close(r.done)
		for ev := range ch {
			r.events = append(r.events, ev)
			if ev.Payload.Kind == stop {
				return
			}
		}
	}()
	return r
}

func (r *permRecorder) wait(t *testing.T) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("bus never delivered the stop event")
	}
}

// End-to-end: a prompted tool call publishes permission_request + the
// requested/granted notifications on the SAME bus, must-deliver, with the
// session id and tool-call id correlated, and the Observe chain stays in
// lockstep (the parity contract).
func TestPermissionPayloadsMustDeliverOnBus(t *testing.T) {
	sess := NewSession("m")
	reg := tools.NewRegistry()
	tool := &trackedTool{name: "file_write", requires: true}
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	gate := tools.NewApprovalGate()
	gate.OnPresentPrompt = func(tools.ApprovalPrompt) (tools.ApprovalChoice, error) {
		return tools.ApprovalOnce, nil
	}
	engine := parallelEngine(sess, reg, gate)
	engine.Bus = pubsub.NewBroker[LoopEvent]()
	defer engine.Bus.Close()

	var observed []LoopEvent
	engine.Observe = func(ev LoopEvent) { observed = append(observed, ev) }
	rec := recordPermBus(t, engine.Bus, EventToolResult)

	m := newTurnMetrics(engine)
	if err := engine.handleToolCalls(context.Background(), callsFor("file_write"), m); err != nil {
		t.Fatalf("handleToolCalls: %v", err)
	}
	rec.wait(t)

	var reqEvents, notifEvents []pubsub.Event[LoopEvent]
	for _, ev := range rec.events {
		switch ev.Payload.Kind {
		case EventPermissionRequest:
			reqEvents = append(reqEvents, ev)
		case EventPermissionNotification:
			notifEvents = append(notifEvents, ev)
		}
	}
	if len(reqEvents) != 1 {
		t.Fatalf("permission_request events = %d, want 1", len(reqEvents))
	}
	ev := reqEvents[0]
	if ev.Type != pubsub.TypePermissionRequest || !ev.MustDeliver {
		t.Fatalf("request event delivery = {Type:%q Must:%v}", ev.Type, ev.MustDeliver)
	}
	req := ev.Payload.PermissionRequest
	if req == nil {
		t.Fatal("request payload missing")
	}
	if req.ToolCallID != "call_0" || req.SessionID != sess.ID() || req.Tool != "file_write" {
		t.Fatalf("payload correlation = %+v (session %q)", req, sess.ID())
	}
	if req.ID == "" {
		t.Fatal("payload id empty")
	}
	if len(notifEvents) != 2 {
		t.Fatalf("notification events = %d, want requested+granted", len(notifEvents))
	}
	for _, ne := range notifEvents {
		if ne.Type != pubsub.TypePermissionNotification || !ne.MustDeliver {
			t.Fatalf("notification delivery = {Type:%q Must:%v}", ne.Type, ne.MustDeliver)
		}
	}
	if n := notifEvents[0].Payload.PermissionNotification; n == nil || !n.Open() || n.ID != req.ID {
		t.Fatalf("first notification = %+v, want open for %q", n, req.ID)
	}
	if n := notifEvents[1].Payload.PermissionNotification; n == nil || !n.Granted || n.ID != req.ID {
		t.Fatalf("second notification = %+v, want granted for %q", n, req.ID)
	}

	// Observe parity: the bus sequence matches the direct recording exactly.
	if len(rec.events) != len(observed) {
		t.Fatalf("bus delivered %d events, Observe saw %d", len(rec.events), len(observed))
	}
	for i, ev := range observed {
		if rec.events[i].Payload.Kind != ev.Kind {
			t.Fatalf("event %d kind differs: bus %v observe %v", i, rec.events[i].Payload.Kind, ev.Kind)
		}
	}
	if execs, _ := tool.stats(); execs != 1 {
		t.Fatalf("approved tool executions = %d, want 1", execs)
	}
}

// A denied prompt publishes the Denied notification; the tool never runs.
func TestPermissionDeniedNotificationOnBus(t *testing.T) {
	sess := NewSession("m")
	reg := tools.NewRegistry()
	tool := &trackedTool{name: "file_write", requires: true}
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	gate := tools.NewApprovalGate()
	gate.OnPresentPrompt = func(tools.ApprovalPrompt) (tools.ApprovalChoice, error) {
		return tools.ApprovalDeny, nil
	}
	engine := parallelEngine(sess, reg, gate)
	engine.Bus = pubsub.NewBroker[LoopEvent]()
	defer engine.Bus.Close()
	rec := recordPermBus(t, engine.Bus, EventToolResult)

	m := newTurnMetrics(engine)
	if err := engine.handleToolCalls(context.Background(), callsFor("file_write"), m); err != nil {
		t.Fatalf("handleToolCalls: %v", err)
	}
	rec.wait(t)

	var last *pubsub.PermissionNotification
	for _, ev := range rec.events {
		if ev.Payload.Kind == EventPermissionNotification {
			last = ev.Payload.PermissionNotification
		}
	}
	if last == nil || !last.Denied {
		t.Fatalf("terminal notification = %+v, want Denied", last)
	}
	if execs, _ := tool.stats(); execs != 0 {
		t.Fatalf("denied tool executed %d times", execs)
	}
}

// Mode switches ride the bus as permission_mode_changed.
func TestPermissionModeChangedOnBus(t *testing.T) {
	sess := NewSession("m")
	gate := tools.NewApprovalGate()
	engine := parallelEngine(sess, tools.NewRegistry(), gate)
	engine.Bus = pubsub.NewBroker[LoopEvent]()
	defer engine.Bus.Close()
	rec := recordPermBus(t, engine.Bus, EventPermissionModeChanged)

	if err := gate.SetMode(tools.ModeYolo); err != nil {
		t.Fatal(err)
	}
	rec.wait(t)
	ev := rec.events[len(rec.events)-1]
	if ev.Type != pubsub.TypePermissionModeChanged || !ev.MustDeliver {
		t.Fatalf("mode event delivery = {Type:%q Must:%v}", ev.Type, ev.MustDeliver)
	}
	c := ev.Payload.PermissionModeChanged
	if c == nil || c.Mode != "yolo" || c.Previous != "normal" {
		t.Fatalf("mode payload = %+v", c)
	}
}

// NewAgentLoop binds the gate to the loop's session: grant keys and payloads
// carry the session id without any manual wiring.
func TestNewAgentLoopBindsGateSession(t *testing.T) {
	sess := NewSession("m")
	gate := tools.NewApprovalGate()
	engine := parallelEngine(sess, tools.NewRegistry(), gate)
	if gate.Bus == nil {
		t.Fatal("gate bus not wired by NewAgentLoop")
	}
	gate.OnPresentPrompt = func(p tools.ApprovalPrompt) (tools.ApprovalChoice, error) {
		return tools.ApprovalGrantExact, nil
	}
	tool := &trackedTool{name: "mutate", requires: true}
	if _, err := gate.Prepare(context.Background(), tool, json.RawMessage(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	keys := gate.SessionGrantKeys()
	if len(keys) != 1 || keys[0].SessionID != sess.ID() {
		t.Fatalf("grant keys = %+v, want session %q", keys, sess.ID())
	}
	_ = engine
}

// A yolo refusal arrives as an ordinary tool result in the transcript — no
// prompt, no modal — and names the way out.
func TestYoloRefusalReachesTranscript(t *testing.T) {
	sess := NewSession("m")
	reg := tools.NewRegistry()
	tool := &trackedTool{name: "shell_exec", requires: true}
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	gate := tools.NewApprovalGate()
	if err := gate.SetMode(tools.ModeYolo); err != nil {
		t.Fatal(err)
	}
	gate.OnPresentPrompt = func(tools.ApprovalPrompt) (tools.ApprovalChoice, error) {
		t.Error("yolo must not prompt")
		return tools.ApprovalDeny, nil
	}
	engine := parallelEngine(sess, reg, gate)
	m := newTurnMetrics(engine)
	calls := []provider.ToolCall{{ID: "c1", Name: "shell_exec", Args: json.RawMessage(`{"command":"rm -rf /"}`)}}
	if err := engine.handleToolCalls(context.Background(), calls, m); err != nil {
		t.Fatalf("handleToolCalls: %v", err)
	}
	var result string
	for _, msg := range sess.History() {
		if msg.Role == "tool" && msg.ToolCallID == "c1" {
			result = msg.Content
		}
	}
	if !strings.Contains(result, "refused in yolo mode") || !strings.Contains(result, "use normal mode or add an allow rule") {
		t.Fatalf("transcript result = %q, want the yolo refusal with the way out", result)
	}
	if execs, _ := tool.stats(); execs != 0 {
		t.Fatalf("refused tool executed %d times", execs)
	}
}
