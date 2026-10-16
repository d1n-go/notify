package notify

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ThrottleParams configures Throttle.
type ThrottleParams struct {
	// Window is the minimum interval between messages with the same key.
	// Zero or negative disables throttling.
	Window time.Duration

	// Key groups messages that throttle each other. Defaults to subject and msg
	// together, i.e. identical messages. Use the subject alone for alerts whose
	// text varies, or a constant to allow at most one message per Window overall.
	Key func(subject, msg string) string
}

// Throttled is a Notifier that drops repeated messages, see Throttle.
type Throttled struct {
	next   Notifier
	window time.Duration
	key    func(subject, msg string) string
	now    func() time.Time

	mu        sync.Mutex
	entries   map[string]*throttleEntry
	lastSweep time.Time
}

type throttleEntry struct {
	sentAt     time.Time
	suppressed int
}

// Throttle wraps n so that a message is sent at most once per Window for its key.
// Suppressed messages are dropped (Send returns nil); their count is appended to
// the next message with the same key that gets through. If sending fails, the
// message does not count, so the next one with the same key is sent right away.
func Throttle(n Notifier, p ThrottleParams) *Throttled {
	key := p.Key
	if key == nil {
		key = func(subject, msg string) string { return subject + "\x00" + msg }
	}
	return &Throttled{
		next:    n,
		window:  p.Window,
		key:     key,
		now:     time.Now,
		entries: make(map[string]*throttleEntry),
	}
}

func (t *Throttled) Send(ctx context.Context, subject string, msg string) error {
	if t.window <= 0 {
		return t.next.Send(ctx, subject, msg)
	}

	k := t.key(subject, msg)
	now := t.now()

	t.mu.Lock()
	t.sweep(now)
	e := t.entries[k]
	if e == nil {
		e = &throttleEntry{}
		t.entries[k] = e
	} else if now.Sub(e.sentAt) < t.window {
		e.suppressed++
		t.mu.Unlock()
		return nil
	}
	prevSentAt, suppressed := e.sentAt, e.suppressed
	e.sentAt, e.suppressed = now, 0
	t.mu.Unlock()

	if suppressed > 0 {
		msg += fmt.Sprintf("\n\n(similar messages suppressed: %d)", suppressed)
	}

	err := t.next.Send(ctx, subject, msg)
	if err != nil {
		t.mu.Lock()
		if t.entries[k] == e && e.sentAt.Equal(now) {
			e.sentAt = prevSentAt
			e.suppressed += suppressed
		}
		t.mu.Unlock()
	}
	return err
}

// sweep drops expired entries without pending suppressed counts. Called with mu held.
func (t *Throttled) sweep(now time.Time) {
	if now.Sub(t.lastSweep) < t.window {
		return
	}
	t.lastSweep = now
	for k, e := range t.entries {
		if e.suppressed == 0 && now.Sub(e.sentAt) >= t.window {
			delete(t.entries, k)
		}
	}
}
