package notify

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/slogtest"
	"time"
)

type sentMessage struct {
	subject, msg string
}

type capturingNotifier struct {
	mu   sync.Mutex
	msgs []sentMessage
	err  error
}

func (c *capturingNotifier) Send(ctx context.Context, subject string, msg string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, sentMessage{subject, msg})
	return c.err
}

func (c *capturingNotifier) sent() []sentMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]sentMessage(nil), c.msgs...)
}

// parseSlogMessage turns a notification back into the map form slogtest expects.
func parseSlogMessage(t *testing.T, m sentMessage) map[string]any {
	t.Helper()

	res := map[string]any{}
	level, msg, ok := strings.Cut(m.subject, ": ")
	if !ok {
		t.Fatalf("unexpected subject %q", m.subject)
	}
	res[slog.LevelKey] = level
	res[slog.MessageKey] = msg

	if m.msg == "" {
		return res
	}
	for _, line := range strings.Split(m.msg, "\n") {
		key, val, ok := strings.Cut(line, ": ")
		if !ok {
			t.Fatalf("unexpected body line %q", line)
		}
		path := strings.Split(key, ".")
		cur := res
		for _, g := range path[:len(path)-1] {
			next, ok := cur[g].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[g] = next
			}
			cur = next
		}
		cur[path[len(path)-1]] = val
	}
	return res
}

func TestSlogHandlerConformance(t *testing.T) {
	var n *capturingNotifier
	slogtest.Run(t,
		func(t *testing.T) slog.Handler {
			n = &capturingNotifier{}
			return NewSlogHandler([]Notifier{n}, &SlogHandlerOptions{Level: slog.LevelDebug})
		},
		func(t *testing.T) map[string]any {
			sent := n.sent()
			if len(sent) != 1 {
				t.Fatalf("expected 1 notification, got %d", len(sent))
			}
			return parseSlogMessage(t, sent[0])
		},
	)
}

func TestSlogHandlerFormat(t *testing.T) {
	n := &capturingNotifier{}
	logger := slog.New(NewSlogHandler([]Notifier{n}, &SlogHandlerOptions{AddSource: true}))

	logger.With("service", "billing").WithGroup("req").Error("payment failed",
		"id", 42,
		slog.Group("user", "name", "ivan"),
		"err", errors.New("timeout"),
	)

	sent := n.sent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(sent))
	}
	if sent[0].subject != "ERROR: payment failed" {
		t.Errorf("unexpected subject %q", sent[0].subject)
	}
	for _, want := range []string{"time: ", "source: ", "slog_test.go:", "service: billing", "req.id: 42", "req.user.name: ivan", "req.err: timeout"} {
		if !strings.Contains(sent[0].msg, want) {
			t.Errorf("body misses %q:\n%s", want, sent[0].msg)
		}
	}
}

func TestSlogHandlerLevel(t *testing.T) {
	n := &capturingNotifier{}
	logger := slog.New(NewSlogHandler([]Notifier{n}, nil))

	logger.Info("info")
	logger.Warn("warn")
	logger.Error("error")

	if sent := n.sent(); len(sent) != 1 || sent[0].subject != "ERROR: error" {
		t.Errorf("expected only error records by default, got %+v", sent)
	}
}

func TestSlogHandlerOnError(t *testing.T) {
	n := &capturingNotifier{err: errors.New("down")}
	var got error
	h := NewSlogHandler([]Notifier{n}, &SlogHandlerOptions{OnError: func(err error) { got = err }})

	slog.New(h).Error("boom")

	if got == nil || !strings.Contains(got.Error(), "down") {
		t.Errorf("expected OnError to receive send error, got %v", got)
	}
}

func TestSlogHandlerIgnoresCallerCancellation(t *testing.T) {
	var gotErr error
	n := notifierFunc(func(ctx context.Context, subject, msg string) error {
		gotErr = ctx.Err()
		return nil
	})
	h := NewSlogHandler([]Notifier{n}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // запрос уже завершён, но уведомление должно уйти

	slog.New(h).ErrorContext(ctx, "boom")

	if gotErr != nil {
		t.Errorf("expected live context for notifier, got %v", gotErr)
	}
}

func TestSlogHandlerAsync(t *testing.T) {
	release := make(chan struct{})
	n := &capturingNotifier{}
	slow := notifierFunc(func(ctx context.Context, subject, msg string) error {
		<-release
		return n.Send(ctx, subject, msg)
	})
	h := NewSlogHandler([]Notifier{slow}, &SlogHandlerOptions{Async: true})

	done := make(chan struct{})
	go func() {
		slog.New(h).Error("boom")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("async log call blocked on sending")
	}

	close(release)
	h.Wait()

	if sent := n.sent(); len(sent) != 1 {
		t.Errorf("expected notification after Wait, got %d", len(sent))
	}
}

type notifierFunc func(ctx context.Context, subject, msg string) error

func (f notifierFunc) Send(ctx context.Context, subject, msg string) error {
	return f(ctx, subject, msg)
}
