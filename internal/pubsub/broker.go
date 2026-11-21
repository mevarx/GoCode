// Package pubsub implements the generic in-process event bus: one
// Broker[T] with three delivery classes — lossy Publish, bounded
// PublishMustDeliver, and coalescing by entity key — plus overflow
// notification for subscribers that must resync after a drop.
//
// Delivery parameters: bufferSize 4096 per subscriber, must-deliver bound 50ms
// per subscriber, at most one pending event per coalescing key per subscriber.
package pubsub

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// BufferSize is the per-subscriber queue capacity.
	BufferSize = 4096
	// MustDeliverTimeout bounds the blocking send of PublishMustDeliver PER
	// SUBSCRIBER. A publisher with N stalled subscribers can wait
	// at most N times this bound, never unbounded.
	MustDeliverTimeout = 50 * time.Millisecond
)

// Event is one bus message. MustDeliver travels ON the event (not on the
// publish call) so fan-in re-publishers preserve the delivery class.
//
// Type is the wire-vocabulary discriminator (see events.go); Key is the
// coalescing entity id — empty means the event never coalesces.
type Event[T any] struct {
	Type        string
	Key         string
	Payload     T
	MustDeliver bool
}

// Broker fans events out to subscribers. The zero value is not usable; call
// NewBroker. All methods are safe for concurrent use.
//
// Structure: each subscriber owns a fixed-capacity ring of Event slots plus a
// key→sequence map. A publish with a coalescing key whose event is still
// queued OVERWRITES that slot in place (keeping its queue position); a new
// key or a keyless event appends at the tail. One pump goroutine per
// subscriber moves ring events to the subscriber's channel in FIFO order.
// An event already handed to the pump is in flight, not pending: coalescing
// applies to queued events, so a subscriber may see one in-flight event for a
// key followed by the coalesced latest — never two queued events for one key.
//
// Drop counters are broker-wide atomics, queryable via DropCount and
// MustDeliverDropCount. Drops are silent (no logging dependency); the
// counters and the overflow channel are the observable signals.
type Broker[T any] struct {
	mu     sync.Mutex
	subs   map[*subscription[T]]struct{}
	closed bool

	bufferSize         int
	mustDeliverTimeout time.Duration

	dropCount            atomic.Int64
	mustDeliverDropCount atomic.Int64
}

// NewBroker returns a broker with the spec-default 4096-slot subscriber
// queues and 50ms must-deliver bound.
func NewBroker[T any]() *Broker[T] {
	return NewBrokerWithBufferSize[T](BufferSize)
}

// NewBrokerWithBufferSize returns a broker with a custom per-subscriber queue
// capacity (minimum 1). The must-deliver bound stays MustDeliverTimeout.
// Small capacities exist for contract tests and memory-constrained embedders.
func NewBrokerWithBufferSize[T any](size int) *Broker[T] {
	if size < 1 {
		size = 1
	}
	return &Broker[T]{
		subs:               make(map[*subscription[T]]struct{}),
		bufferSize:         size,
		mustDeliverTimeout: MustDeliverTimeout,
	}
}

// subscription is one subscriber's queue and delivery pump. Ring state is
// guarded by b.mu; the channels are the lock-free signalling paths.
type subscription[T any] struct {
	b        *Broker[T]
	ch       chan Event[T]
	overflow chan struct{} // non-nil only for SubscribeWithOverflow
	once     sync.Once     // closes overflow on the first drop
	wake     chan struct{} // cap 1: ring became non-empty or subscriber stopped
	space    chan struct{} // cap 1: ring freed a slot (must-deliver waiters)
	done     chan struct{} // closed once to stop the pump
	stopOnce sync.Once

	// Ring state, guarded by b.mu. head/tail are absolute sequence numbers;
	// slot = seq % len(ring). keys maps a coalescing key to the sequence of
	// its single queued event.
	ring   []Event[T]
	head   uint64
	tail   uint64
	keys   map[string]uint64
	closed bool
}

func (s *subscription[T]) count() uint64 { return s.tail - s.head }

// signalWake / signalSpace are non-blocking notifies; the cap-1 channels
// coalesce repeated signals into one wakeup.
func (s *subscription[T]) signalWake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *subscription[T]) signalSpace() {
	select {
	case s.space <- struct{}{}:
	default:
	}
}

func (s *subscription[T]) stop() {
	s.stopOnce.Do(func() { close(s.done) })
}

// noteDrop records a lossy drop and closes the overflow channel once.
func (s *subscription[T]) noteDrop() {
	s.b.dropCount.Add(1)
	s.closeOverflow()
}

func (s *subscription[T]) closeOverflow() {
	if s.overflow != nil {
		s.once.Do(func() { close(s.overflow) })
	}
}

// enqueueLocked appends ev or overwrites its key's queued slot. It reports
// whether the event was accepted (false = queue full, caller must drop).
// b.mu must be held.
func (s *subscription[T]) enqueueLocked(ev Event[T]) bool {
	if ev.Key != "" {
		if seq, ok := s.keys[ev.Key]; ok {
			slot := seq % uint64(len(s.ring))
			// MustDeliver is sticky across coalescing: overwriting a pending
			// must-deliver event must not silently downgrade its guarantee.
			ev.MustDeliver = ev.MustDeliver || s.ring[slot].MustDeliver
			s.ring[slot] = ev
			return true
		}
	}
	if s.count() == uint64(len(s.ring)) {
		return false
	}
	slot := s.tail % uint64(len(s.ring))
	s.ring[slot] = ev
	if ev.Key != "" {
		s.keys[ev.Key] = s.tail
	}
	s.tail++
	s.signalWake()
	return true
}

// popLocked removes the head event. b.mu must be held.
func (s *subscription[T]) popLocked() Event[T] {
	ev := s.ring[s.head%uint64(len(s.ring))]
	if ev.Key != "" {
		delete(s.keys, ev.Key)
	}
	s.head++
	s.signalSpace()
	return ev
}

// pump moves queued events to the subscriber channel until stopped. It owns
// closing ch, so consumers see exactly one closure on unsubscribe or Close.
func (s *subscription[T]) pump() {
	defer close(s.ch)
	for {
		s.b.mu.Lock()
		for s.count() == 0 && !s.closed {
			s.b.mu.Unlock()
			select {
			case <-s.wake:
			case <-s.done:
			}
			s.b.mu.Lock()
		}
		if s.closed {
			s.b.mu.Unlock()
			return
		}
		ev := s.popLocked()
		s.b.mu.Unlock()

		select {
		case s.ch <- ev:
		case <-s.done:
			return
		}
	}
}

// Subscribe registers a subscriber and returns its event channel and an
// idempotent unsubscribe func. After Close the returned channel is already
// closed. The consumer must keep draining: a full queue drops lossy events.
func Subscribe[T any](b *Broker[T]) (<-chan Event[T], func()) {
	events, _, unsub := subscribe(b, false)
	return events, unsub
}

// SubscribeWithOverflow additionally returns a channel closed on the FIRST
// drop (lossy or must-deliver timeout), signalling that the subscriber missed
// events and should resync. It is not closed again on later drops.
func SubscribeWithOverflow[T any](b *Broker[T]) (<-chan Event[T], <-chan struct{}, func()) {
	return subscribe(b, true)
}

// Subscribe is the method form of Subscribe.
func (b *Broker[T]) Subscribe() (<-chan Event[T], func()) {
	return Subscribe(b)
}

// SubscribeWithOverflow is the method form of SubscribeWithOverflow.
func (b *Broker[T]) SubscribeWithOverflow() (<-chan Event[T], <-chan struct{}, func()) {
	return SubscribeWithOverflow(b)
}

func subscribe[T any](b *Broker[T], withOverflow bool) (<-chan Event[T], <-chan struct{}, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		ch := make(chan Event[T])
		close(ch)
		var ov chan struct{}
		if withOverflow {
			ov = make(chan struct{})
			close(ov)
		}
		return ch, ov, func() {}
	}
	s := &subscription[T]{
		b:     b,
		ch:    make(chan Event[T]),
		wake:  make(chan struct{}, 1),
		space: make(chan struct{}, 1),
		done:  make(chan struct{}),
		ring:  make([]Event[T], b.bufferSize),
		keys:  make(map[string]uint64),
	}
	if withOverflow {
		s.overflow = make(chan struct{})
	}
	b.subs[s] = struct{}{}
	go s.pump()
	unsub := func() {
		b.mu.Lock()
		if s.closed {
			b.mu.Unlock()
			return
		}
		s.closed = true
		delete(b.subs, s)
		s.stop()
		s.signalWake()
		b.mu.Unlock()
	}
	return s.ch, s.overflow, unsub
}

// Publish delivers ev to every subscriber without blocking. A subscriber
// whose queue is full drops the event and increments DropCount. Events with
// a non-empty Key coalesce: at most one queued event per key per subscriber,
// the newest overwriting in place at the old queue position. Publishing to a
// closed broker is a no-op.
func (b *Broker[T]) Publish(ev Event[T]) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	for s := range b.subs {
		if !s.enqueueLocked(ev) {
			s.noteDrop()
		}
	}
}

// PublishMustDeliver delivers terminal events (turn end, tool result, error,
// exit, permission prompts). It first attempts a non-blocking enqueue per
// subscriber; a full queue gets a blocking retry bounded by
// MustDeliverTimeout PER SUBSCRIBER, honouring ctx. On timeout or cancellation
// the event is dropped for that subscriber and MustDeliverDropCount is
// incremented. The publisher is never blocked beyond the per-subscriber bound
// (plus scheduling), and a closed broker is a no-op.
func (b *Broker[T]) PublishMustDeliver(ctx context.Context, ev Event[T]) {
	ev.MustDeliver = true
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	subs := make([]*subscription[T], 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.Unlock()

	for _, s := range subs {
		b.deliverMust(ctx, s, ev)
	}
}

func (b *Broker[T]) deliverMust(ctx context.Context, s *subscription[T], ev Event[T]) {
	deadline := time.Now().Add(b.mustDeliverTimeout)
	for {
		b.mu.Lock()
		if b.closed || s.closed {
			b.mu.Unlock()
			return
		}
		if s.enqueueLocked(ev) {
			b.mu.Unlock()
			return
		}
		b.mu.Unlock()

		// Queue full: wait for a freed slot, the per-subscriber deadline, or
		// cancellation — whichever comes first.
		wait := time.Until(deadline)
		if wait <= 0 {
			b.mustDeliverDrop(s)
			return
		}
		timer := time.NewTimer(wait)
		select {
		case <-s.space:
			timer.Stop()
		case <-s.wake:
			// A wake can also mean the subscriber stopped; re-check under lock.
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			b.mustDeliverDrop(s)
			return
		case <-timer.C:
			b.mustDeliverDrop(s)
			return
		}
	}
}

func (b *Broker[T]) mustDeliverDrop(s *subscription[T]) {
	b.mustDeliverDropCount.Add(1)
	s.closeOverflow()
}

// DropCount reports lossy drops across all subscribers since creation.
func (b *Broker[T]) DropCount() int64 { return b.dropCount.Load() }

// MustDeliverDropCount reports must-deliver timeouts/cancellations across all
// subscribers since creation. A non-zero value means a terminal event was
// lost to a stalled subscriber — the overflow channel flags the same loss.
func (b *Broker[T]) MustDeliverDropCount() int64 { return b.mustDeliverDropCount.Load() }

// Close stops delivery and closes every subscriber channel. It is idempotent;
// Publish and PublishMustDeliver afterwards are no-ops, and Subscribe returns
// an already-closed channel. Pumps exit promptly, so no goroutine outlives
// the broker.
func (b *Broker[T]) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for s := range b.subs {
		s.closed = true
		s.stop()
		s.signalWake()
	}
	b.subs = make(map[*subscription[T]]struct{})
}
