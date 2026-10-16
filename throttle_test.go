package notify

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingNotifier struct {
	mu   sync.Mutex
	msgs []string
	err  error
}

func (r *recordingNotifier) Send(ctx context.Context, subject string, msg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, msg)
	return r.err
}

func (r *recordingNotifier) sent() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.msgs...)
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestThrottle(n Notifier, p ThrottleParams) (*Throttled, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	th := Throttle(n, p)
	th.now = clock.now
	return th, clock
}

func TestThrottleSuppressesDuplicates(t *testing.T) {
	rec := &recordingNotifier{}
	th, clock := newTestThrottle(rec, ThrottleParams{Window: time.Minute})
	ctx := context.Background()

	for range 3 {
		if err := th.Send(ctx, "s", "disk full"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		clock.advance(10 * time.Second)
	}
	if got := rec.sent(); len(got) != 1 {
		t.Fatalf("expected 1 message within window, got %d: %q", len(got), got)
	}

	clock.advance(time.Minute)
	_ = th.Send(ctx, "s", "disk full")

	got := rec.sent()
	if len(got) != 2 {
		t.Fatalf("expected message after window, got %d", len(got))
	}
	if !strings.HasSuffix(got[1], "(similar messages suppressed: 2)") {
		t.Errorf("expected suppressed count, got %q", got[1])
	}
}

func TestThrottleDifferentMessagesPass(t *testing.T) {
	rec := &recordingNotifier{}
	th, _ := newTestThrottle(rec, ThrottleParams{Window: time.Minute})
	ctx := context.Background()

	_ = th.Send(ctx, "s", "a")
	_ = th.Send(ctx, "s", "b")
	_ = th.Send(ctx, "other", "a")

	if got := rec.sent(); len(got) != 3 {
		t.Errorf("expected 3 distinct messages, got %d", len(got))
	}
}

func TestThrottleCustomKey(t *testing.T) {
	rec := &recordingNotifier{}
	th, _ := newTestThrottle(rec, ThrottleParams{
		Window: time.Minute,
		Key:    func(subject, msg string) string { return "" }, // одно сообщение в минуту на всё
	})
	ctx := context.Background()

	_ = th.Send(ctx, "a", "1")
	_ = th.Send(ctx, "b", "2")

	if got := rec.sent(); len(got) != 1 {
		t.Errorf("expected global limit of 1 message, got %d", len(got))
	}
}

func TestThrottleFailedSendDoesNotCount(t *testing.T) {
	rec := &recordingNotifier{err: errors.New("down")}
	th, clock := newTestThrottle(rec, ThrottleParams{Window: time.Minute})
	ctx := context.Background()

	if err := th.Send(ctx, "s", "m"); err == nil {
		t.Fatal("expected error from wrapped notifier")
	}
	clock.advance(time.Second)

	rec.err = nil
	if err := th.Send(ctx, "s", "m"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := rec.sent(); len(got) != 2 {
		t.Errorf("expected retry right after failure, got %d sends", len(got))
	}

	_ = th.Send(ctx, "s", "m")
	if got := rec.sent(); len(got) != 2 {
		t.Errorf("expected throttling after successful send, got %d sends", len(got))
	}
}

func TestThrottleKeepsSuppressedCountAfterFailure(t *testing.T) {
	rec := &recordingNotifier{}
	th, clock := newTestThrottle(rec, ThrottleParams{Window: time.Minute})
	ctx := context.Background()

	_ = th.Send(ctx, "s", "m")
	_ = th.Send(ctx, "s", "m") // suppressed
	clock.advance(time.Minute)

	rec.err = errors.New("down")
	_ = th.Send(ctx, "s", "m") // fails, carries "1 suppressed"

	rec.err = nil
	_ = th.Send(ctx, "s", "m")

	got := rec.sent()
	if last := got[len(got)-1]; !strings.HasSuffix(last, "(similar messages suppressed: 1)") {
		t.Errorf("expected suppressed count to survive failed send, got %q", last)
	}
}

func TestThrottleSweepsExpiredEntries(t *testing.T) {
	th, clock := newTestThrottle(&recordingNotifier{}, ThrottleParams{Window: time.Minute})
	ctx := context.Background()

	_ = th.Send(ctx, "s", "a")
	_ = th.Send(ctx, "s", "b")
	clock.advance(2 * time.Minute)
	_ = th.Send(ctx, "s", "c")

	if n := len(th.entries); n != 1 {
		t.Errorf("expected expired entries to be swept, got %d entries", n)
	}
}

func TestThrottleDisabled(t *testing.T) {
	rec := &recordingNotifier{}
	th := Throttle(rec, ThrottleParams{})

	_ = th.Send(context.Background(), "s", "m")
	_ = th.Send(context.Background(), "s", "m")

	if got := rec.sent(); len(got) != 2 {
		t.Errorf("expected no throttling with zero window, got %d", len(got))
	}
}

func TestThrottleConcurrent(t *testing.T) {
	rec := &recordingNotifier{}
	th := Throttle(rec, ThrottleParams{Window: time.Hour})

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = th.Send(context.Background(), "s", "m")
		}()
	}
	wg.Wait()

	if got := rec.sent(); len(got) != 1 {
		t.Errorf("expected exactly 1 message from concurrent sends, got %d", len(got))
	}
}
