package pubsub

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"
)

// drain collects events until the channel closes or the deadline passes.
// Generous deadlines keep the suite Windows-safe (no real-time assertions).
func drain[T any](t *testing.T, ch <-chan Event[T], deadline time.Duration) []Event[T] {
	t.Helper()
	var got []Event[T]
	timeout := time.After(deadline)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-timeout:
			return got
		}
	}
}

// drainN collects exactly n events (failing on shortage or closure), then
// asserts no straggler arrives — coalescing must leave no second pending
// event for a key.
func drainN[T any](t *testing.T, ch <-chan Event[T], n int, deadline time.Duration) []Event[T] {
	t.Helper()
	got := make([]Event[T], 0, n)
	timeout := time.After(deadline)
	for len(got) < n {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed after %d/%d events", len(got), n)
			}
			got = append(got, ev)
		case <-timeout:
			t.Fatalf("only %d/%d events arrived", len(got), n)
		}
	}
	select {
	case ev := <-ch:
		t.Fatalf("unexpected extra event: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
	return got
}

// fillUntilOverflow publishes keyless events until the subscriber's overflow
// channel closes, i.e. a drop proves the ring hit capacity.
func fillUntilOverflow[T any](t *testing.T, b *Broker[T], overflow <-chan struct{}) int {
	t.Helper()
	for i := 0; ; i++ {
		select {
		case <-overflow:
			return i
		default:
		}
		if i > 100*BufferSize {
			t.Fatal("queue never overflowed")
		}
		b.Publish(Event[T]{Type: TypeAgentEvent})
	}
}

// stallSubscribers provably parks every subscriber: ring full AND the pump
// blocked sending the head (no consumer). fillUntilOverflow alone is not
// enough — a pump may not have popped yet, and its first pop would free a
// slot mid-wait. Each pop signals space, so waiting for that signal
// synchronizes with the in-flight handoff; one refill restores capacity for
// every subscriber at once.
func stallSubscribers[T any](t *testing.T, b *Broker[T], overflow <-chan struct{}) {
	t.Helper()
	fillUntilOverflow(t, b, overflow)
	b.mu.Lock()
	subs := make([]*subscription[T], 0, len(b.subs))
	for sub := range b.subs {
		subs = append(subs, sub)
	}
	b.mu.Unlock()
	if len(subs) == 0 {
		t.Fatal("no subscribers to stall")
	}
	for _, s := range subs {
		select {
		case <-s.space:
		case <-time.After(2 * time.Second):
			t.Fatal("pump never took the head in flight")
		}
	}
	// Each pop freed one slot; no pump can pop again until its send
	// completes, and nobody receives. Refill: every ring is now stably full.
	b.Publish(Event[T]{Type: TypeAgentEvent})
}

func TestPublishLossyOverflowDropsWithoutBlocking(t *testing.T) {
	b := NewBrokerWithBufferSize[int](8)
	defer b.Close()
	events, overflow, unsub := b.SubscribeWithOverflow()
	defer unsub()

	const published = 200
	start := time.Now()
	for i := 0; i < published; i++ {
		b.Publish(Event[int]{Type: TypeAgentEvent, Payload: i})
	}
	elapsed := time.Since(start)
	// Publish is non-blocking: 200 publishes must not approach the 50ms
	// must-deliver bound, let alone block. Generous upper bound for CI noise.
	if elapsed > time.Second {
		t.Fatalf("lossy Publish blocked: %v for %d events", elapsed, published)
	}

	select {
	case <-overflow:
	case <-time.After(2 * time.Second):
		t.Fatal("overflow channel never closed on drop")
	}

	got := drain(t, events, 2*time.Second)
	// The ring holds 8; the pump may hold one in flight, so 8 or 9 survive.
	if len(got) < 8 || len(got) > 9 {
		t.Fatalf("accepted %d events, want 8 or 9 (ring + in-flight)", len(got))
	}
	if drops := b.DropCount(); drops != int64(published-len(got)) {
		t.Fatalf("DropCount = %d, want %d", drops, published-len(got))
	}
	if b.MustDeliverDropCount() != 0 {
		t.Fatalf("lossy drops incremented MustDeliverDropCount")
	}
	// FIFO delivery: payloads strictly increase. The accepted set need not
	// be a contiguous prefix — if the pump takes the head in flight mid-loop,
	// its freed slot can admit one later event (still delivered in order).
	for i := 1; i < len(got); i++ {
		if got[i].Payload <= got[i-1].Payload {
			t.Fatalf("FIFO violated at %d: %d after %d", i, got[i].Payload, got[i-1].Payload)
		}
	}
}

func TestPublishMustDeliverReachesSlowSubscriber(t *testing.T) {
	b := NewBrokerWithBufferSize[int](4)
	defer b.Close()
	events, overflow, unsub := b.SubscribeWithOverflow()
	defer unsub()
	fillUntilOverflow(t, b, overflow)

	// A slow-but-alive subscriber: drain one event, then keep draining.
	go func() {
		for range events {
		}
	}()

	start := time.Now()
	b.PublishMustDeliver(context.Background(), Event[int]{Type: TypeRunComplete, Payload: 999, MustDeliver: true})
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("PublishMustDeliver exceeded its bound: %v", elapsed)
	}
	if b.MustDeliverDropCount() != 0 {
		t.Fatalf("must-deliver dropped for a draining subscriber")
	}
}

// Same contract, deterministic delivery assertion: the consumer drains into
// a slice and the terminal event must be present.
func TestPublishMustDeliverDeliveredWithinBound(t *testing.T) {
	b := NewBrokerWithBufferSize[int](4)
	defer b.Close()
	events, overflow, unsub := b.SubscribeWithOverflow()
	defer unsub()
	fillUntilOverflow(t, b, overflow)

	gotCh := make(chan []Event[int], 1)
	go func() {
		var got []Event[int]
		for ev := range events {
			got = append(got, ev)
			if ev.Payload == 999 {
				break
			}
		}
		gotCh <- got
	}()

	start := time.Now()
	b.PublishMustDeliver(context.Background(), Event[int]{Type: TypeRunComplete, Payload: 999, MustDeliver: true})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("PublishMustDeliver blocked %v", elapsed)
	}

	select {
	case got := <-gotCh:
		last := got[len(got)-1]
		if last.Payload != 999 || !last.MustDeliver {
			t.Fatalf("terminal event not delivered intact: %+v", last)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("terminal event never arrived at a draining subscriber")
	}
	if b.MustDeliverDropCount() != 0 {
		t.Fatalf("unexpected must-deliver drop")
	}
}

func TestPublishMustDeliverStalledSubscriberDropsAfterBound(t *testing.T) {
	b := NewBrokerWithBufferSize[int](4)
	defer b.Close()
	_, overflow, unsub := b.SubscribeWithOverflow()
	defer unsub()
	stallSubscribers(t, b, overflow)
	dropsBefore := b.DropCount()

	// Nobody drains: the must-deliver attempt must time out per subscriber.
	start := time.Now()
	b.PublishMustDeliver(context.Background(), Event[int]{Type: TypeRunComplete, Payload: 1, MustDeliver: true})
	elapsed := time.Since(start)

	// The bound is 50ms per subscriber. Windows timer granularity is ~15ms,
	// so allow generous slack on both sides; the contract is "bounded, not
	// instant, not forever".
	if elapsed < 30*time.Millisecond {
		t.Fatalf("must-deliver gave up too early: %v", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("must-deliver exceeded its bound: %v", elapsed)
	}
	if b.MustDeliverDropCount() != 1 {
		t.Fatalf("MustDeliverDropCount = %d, want 1", b.MustDeliverDropCount())
	}
	if b.DropCount() != dropsBefore {
		t.Fatalf("must-deliver timeout incremented the lossy DropCount")
	}
	// The must-deliver drop is a drop: overflow stays closed (it closed on
	// the first lossy drop while filling; the contract is first-drop closure,
	// asserted in isolation by TestSubscribeWithOverflowClosesOnFirstDropOnly).
	select {
	case <-overflow:
	case <-time.After(2 * time.Second):
		t.Fatal("overflow not closed after must-deliver drop")
	}
}

func TestPublishMustDeliverBoundIsPerSubscriber(t *testing.T) {
	b := NewBrokerWithBufferSize[int](2)
	defer b.Close()
	_, ov1, u1 := b.SubscribeWithOverflow()
	defer u1()
	_, ov2, u2 := b.SubscribeWithOverflow()
	defer u2()
	stallSubscribers(t, b, ov1)
	// Both queues are full (same publish stream); the second overflow closed
	// on the same first drop.
	select {
	case <-ov2:
	case <-time.After(2 * time.Second):
		t.Fatal("second subscriber never overflowed")
	}

	start := time.Now()
	b.PublishMustDeliver(context.Background(), Event[int]{Payload: 7, MustDeliver: true})
	elapsed := time.Since(start)
	// Two stalled subscribers: ~2×50ms, still bounded well under a second.
	if elapsed > 2*time.Second {
		t.Fatalf("per-subscriber bound not honoured: %v", elapsed)
	}
	if b.MustDeliverDropCount() != 2 {
		t.Fatalf("MustDeliverDropCount = %d, want 2 (one per stalled subscriber)", b.MustDeliverDropCount())
	}
}

func TestPublishMustDeliverHonoursContext(t *testing.T) {
	b := NewBrokerWithBufferSize[int](2)
	defer b.Close()
	_, overflow, unsub := b.SubscribeWithOverflow()
	defer unsub()
	stallSubscribers(t, b, overflow)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	b.PublishMustDeliver(ctx, Event[int]{Payload: 1, MustDeliver: true})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancelled context did not short-circuit: %v", elapsed)
	}
	if b.MustDeliverDropCount() != 1 {
		t.Fatalf("cancelled must-deliver not counted as a drop")
	}
}

func TestPublishMustDeliverFastPathNonBlocking(t *testing.T) {
	b := NewBroker[int]()
	defer b.Close()
	events, unsub := b.Subscribe()
	defer unsub()

	start := time.Now()
	b.PublishMustDeliver(context.Background(), Event[int]{Type: TypeRunComplete, Payload: 42, MustDeliver: true})
	if elapsed := time.Since(start); elapsed > 30*time.Millisecond {
		t.Fatalf("empty-queue must-deliver was not non-blocking: %v", elapsed)
	}
	select {
	case ev := <-events:
		if ev.Payload != 42 || !ev.MustDeliver {
			t.Fatalf("bad terminal event: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal event not delivered")
	}
}

// blockHead publishes one keyless event and waits until the pump has taken
// it in flight, so subsequent publishes provably stay queued (coalescible).
func blockHead(t *testing.T, b *Broker[string], events <-chan Event[string]) {
	t.Helper()
	b.Publish(Event[string]{Type: TypeAgentEvent, Payload: "head"})
	// The pump pops the head and blocks sending it (no consumer yet). Give
	// it a moment to take the event in flight; the assertions below hold
	// whether or not it has, because the head is keyless and never coalesces.
	time.Sleep(20 * time.Millisecond)
	_ = events
}

func TestCoalescingKeepsLatestPayloadAtQueuePosition(t *testing.T) {
	b := NewBrokerWithBufferSize[string](16)
	defer b.Close()
	events, unsub := b.Subscribe()
	defer unsub()
	blockHead(t, b, events)

	// 50 updates for one key, then a distinct key, then a keyless event.
	for i := 0; i < 50; i++ {
		b.Publish(Event[string]{Type: TypeMessage, Key: "msg", Payload: fmt.Sprintf("snap-%d", i)})
	}
	b.Publish(Event[string]{Type: TypeMessage, Key: "other", Payload: "other-1"})
	b.Publish(Event[string]{Type: TypeAgentEvent, Payload: "tail"})

	want := []string{"head", "snap-49", "other-1", "tail"}
	got := drainN(t, events, len(want), 2*time.Second)
	if len(got) != len(want) {
		t.Fatalf("got %d events (%v), want %d — coalescing must keep ONE pending per key", len(got), payloads(got), len(want))
	}
	for i, w := range want {
		if got[i].Payload != w {
			t.Fatalf("position %d: got %q, want %q (queue position must be preserved)", i, got[i].Payload, w)
		}
	}
	if b.DropCount() != 0 {
		t.Fatalf("coalescing counted as drops: %d", b.DropCount())
	}
}

func TestCoalescingDistinctKeysStayFIFO(t *testing.T) {
	b := NewBrokerWithBufferSize[string](16)
	defer b.Close()
	events, unsub := b.Subscribe()
	defer unsub()
	blockHead(t, b, events)

	b.Publish(Event[string]{Key: "a", Payload: "a1"})
	b.Publish(Event[string]{Key: "b", Payload: "b1"})
	b.Publish(Event[string]{Key: "a", Payload: "a2"}) // overwrites a1 in place
	b.Publish(Event[string]{Key: "c", Payload: "c1"})
	b.Publish(Event[string]{Key: "b", Payload: "b2"}) // overwrites b1 in place

	want := []string{"head", "a2", "b2", "c1"}
	got := drainN(t, events, len(want), 2*time.Second)
	for i, w := range want {
		if got[i].Payload != w {
			t.Fatalf("position %d: got %q, want %q — distinct keys must not reorder", i, got[i].Payload, w)
		}
	}
}

func TestCoalescingMustDeliverIsSticky(t *testing.T) {
	b := NewBrokerWithBufferSize[string](4)
	defer b.Close()
	events, unsub := b.Subscribe()
	defer unsub()
	blockHead(t, b, events)

	// A lossy snapshot pending, then a must-deliver update for the same key.
	b.Publish(Event[string]{Key: "k", Payload: "lossy"})
	b.PublishMustDeliver(context.Background(), Event[string]{Key: "k", Payload: "terminal", MustDeliver: true})
	// And the reverse: a later lossy update must not downgrade the guarantee.
	b.Publish(Event[string]{Key: "k", Payload: "newest"})

	got := drainN(t, events, 2, 2*time.Second)
	if got[1].Payload != "newest" || !got[1].MustDeliver {
		t.Fatalf("coalesced event lost payload or MustDeliver stickiness: %+v", got[1])
	}
}

func TestSubscribeWithOverflowClosesOnFirstDropOnly(t *testing.T) {
	b := NewBrokerWithBufferSize[int](2)
	defer b.Close()
	_, overflow, unsub := b.SubscribeWithOverflow()
	defer unsub()

	// No drop yet: overflow must be open.
	select {
	case <-overflow:
		t.Fatal("overflow closed before any drop")
	default:
	}

	fillUntilOverflow(t, b, overflow)
	// Closed: every receive succeeds immediately with ok=false.
	for i := 0; i < 3; i++ {
		select {
		case _, ok := <-overflow:
			if ok {
				t.Fatal("overflow delivered a value; it must only close")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("overflow not closed after first drop")
		}
	}
	// More drops do not panic or re-signal (closed channel stays closed).
	for i := 0; i < 10; i++ {
		b.Publish(Event[int]{Payload: i})
	}
	if b.DropCount() < 10 {
		t.Fatalf("later drops not counted: %d", b.DropCount())
	}
}

func TestOrderingFIFOWithLiveConsumer(t *testing.T) {
	// The default 4096 queue holds the whole sequence, so a fast publisher
	// cannot drop even before the consumer goroutine is scheduled.
	b := NewBroker[int]()
	defer b.Close()
	events, unsub := b.Subscribe()
	defer unsub()

	const n = 2000
	go func() {
		for i := 0; i < n; i++ {
			b.Publish(Event[int]{Type: TypeAgentEvent, Payload: i})
		}
	}()

	got := make([]int, 0, n)
	deadline := time.After(5 * time.Second)
	for len(got) < n {
		select {
		case ev := <-events:
			got = append(got, ev.Payload)
		case <-deadline:
			t.Fatalf("only %d/%d events arrived", len(got), n)
		}
	}
	for i, v := range got {
		if v != i {
			t.Fatalf("FIFO violated at %d: got %d", i, v)
		}
	}
	if b.DropCount() != 0 {
		t.Fatalf("live consumer lost events: %d drops", b.DropCount())
	}
}

func TestCloseStopsDeliveryAndPublishIsNoop(t *testing.T) {
	b := NewBroker[int]()
	events, unsub := b.Subscribe()
	b.Publish(Event[int]{Payload: 1})
	b.Close()
	b.Close() // idempotent

	// The channel closes (pending events may or may not flush; the contract
	// is closure, not drain).
	select {
	case _, ok := <-events:
		if ok {
			// One in-flight event is fine; the next receive must close.
			select {
			case _, ok2 := <-events:
				if ok2 {
					t.Fatal("channel not closed after Close")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("channel never closed after Close")
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel never closed after Close")
	}

	drops := b.DropCount()
	b.Publish(Event[int]{Payload: 2})                                                     // no-op
	b.PublishMustDeliver(context.Background(), Event[int]{Payload: 3, MustDeliver: true}) // no-op
	if b.DropCount() != drops || b.MustDeliverDropCount() != 0 {
		t.Fatal("publish after Close changed counters")
	}
	unsub() // safe after Close

	// Subscribe after Close yields an already-closed channel.
	ch2, unsub2 := b.Subscribe()
	defer unsub2()
	select {
	case _, ok := <-ch2:
		if ok {
			t.Fatal("post-Close subscribe delivered an event")
		}
	default:
		t.Fatal("post-Close subscribe channel not closed")
	}
}

func TestUnsubscribeClosesOnlyThatSubscriber(t *testing.T) {
	b := NewBroker[int]()
	defer b.Close()
	ch1, u1 := b.Subscribe()
	ch2, u2 := b.Subscribe()
	defer u2()

	u1()
	u1() // idempotent
	select {
	case _, ok := <-ch1:
		if ok {
			t.Fatal("unsubscribed channel still open")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unsubscribed channel never closed")
	}

	b.Publish(Event[int]{Payload: 5})
	select {
	case ev := <-ch2:
		if ev.Payload != 5 {
			t.Fatalf("other subscriber affected: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remaining subscriber stopped receiving")
	}
}

func TestNoGoroutineLeakOnCloseAndUnsubscribe(t *testing.T) {
	settle := func(baseline int) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for runtime.NumGoroutine() > baseline {
			if time.Now().After(deadline) {
				t.Fatalf("goroutines did not settle: %d > baseline %d", runtime.NumGoroutine(), baseline)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	// Let any earlier test's goroutines finish first.
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	b := NewBroker[int]()
	var unsubs []func()
	for i := 0; i < 5; i++ {
		_, u := b.Subscribe()
		unsubs = append(unsubs, u)
	}
	for i := 0; i < 100; i++ {
		b.Publish(Event[int]{Payload: i})
	}
	b.Close()
	for _, u := range unsubs {
		u()
	}
	settle(baseline)

	// Unsubscribe path without Close.
	b2 := NewBroker[int]()
	_, u := b2.Subscribe()
	b2.Publish(Event[int]{Payload: 1})
	u()
	settle(baseline)
	b2.Close()
	settle(baseline)
}

func TestPermissionHelpersPublishMustDeliver(t *testing.T) {
	// Nil brokers are no-ops (incremental wiring).
	PublishPermissionRequest(context.Background(), nil, PermissionRequest{ID: "p1"})
	PublishPermissionNotification(context.Background(), nil, PermissionNotification{ID: "p1"})

	reqBus := NewBroker[PermissionRequest]()
	defer reqBus.Close()
	reqCh, reqUnsub := reqBus.Subscribe()
	defer reqUnsub()

	notifBus := NewBroker[PermissionNotification]()
	defer notifBus.Close()
	notifCh, notifUnsub := notifBus.Subscribe()
	defer notifUnsub()

	req := PermissionRequest{ID: "p1", Tool: "shell_exec", Args: []byte(`{"command":"ls"}`)}
	PublishPermissionRequest(context.Background(), reqBus, req)
	select {
	case ev := <-reqCh:
		if ev.Type != TypePermissionRequest || !ev.MustDeliver || ev.Payload.ID != "p1" {
			t.Fatalf("bad request event: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("permission request not delivered")
	}

	// The all-false state is an open prompt; transitions are must-deliver.
	open := PermissionNotification{ID: "p1"}
	if !open.Open() {
		t.Fatal("all-false notification must report Open")
	}
	PublishPermissionNotification(context.Background(), notifBus, open)
	PublishPermissionNotification(context.Background(), notifBus, PermissionNotification{ID: "p1", Granted: true})
	var states []PermissionNotification
	for i := 0; i < 2; i++ {
		select {
		case ev := <-notifCh:
			if ev.Type != TypePermissionNotification || !ev.MustDeliver {
				t.Fatalf("bad notification event: %+v", ev)
			}
			states = append(states, ev.Payload)
		case <-time.After(2 * time.Second):
			t.Fatal("permission notification not delivered")
		}
	}
	if !states[0].Open() || !states[1].Granted {
		t.Fatalf("state transitions lost or reordered: %+v", states)
	}
	if reqBus.MustDeliverDropCount() != 0 || notifBus.MustDeliverDropCount() != 0 {
		t.Fatal("permission lanes dropped events")
	}
}

func payloads(got []Event[string]) []string {
	out := make([]string, len(got))
	for i, ev := range got {
		out[i] = ev.Payload
	}
	return out
}
