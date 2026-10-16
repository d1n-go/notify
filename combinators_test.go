package notify

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMultiSendsInParallel(t *testing.T) {
	// оба уведомителя ждут друг друга: при последовательной отправке тест зависнет
	var started sync.WaitGroup
	started.Add(2)
	n := notifierFunc(func(ctx context.Context, m Message) error {
		started.Done()
		started.Wait()
		return nil
	})

	done := make(chan error, 1)
	go func() { done <- Multi(n, n).Send(context.Background(), Message{}) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Multi sends sequentially")
	}
}

func TestMultiJoinsErrorsInOrder(t *testing.T) {
	errA, errC := errors.New("a failed"), errors.New("c failed")
	slowA := notifierFunc(func(ctx context.Context, m Message) error {
		time.Sleep(20 * time.Millisecond)
		return errA
	})
	b, c := &mockNotifier{}, &mockNotifier{err: errC}

	err := Multi(slowA, b, c).Send(context.Background(), Message{})
	if err == nil || err.Error() != "a failed\nc failed" {
		t.Fatalf("expected errors joined in notifier order, got %v", err)
	}
	if b.calls != 1 || c.calls != 1 {
		t.Errorf("expected every notifier to be called, got %d and %d", b.calls, c.calls)
	}
}

func TestMultiEmpty(t *testing.T) {
	if err := Multi().Send(context.Background(), Message{}); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestFallbackStopsAtFirstSuccess(t *testing.T) {
	a, b, c := &mockNotifier{err: errors.New("down")}, &mockNotifier{}, &mockNotifier{}

	if err := Fallback(a, b, c).Send(context.Background(), Message{}); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if a.calls != 1 || b.calls != 1 || c.calls != 0 {
		t.Errorf("unexpected calls: %d, %d, %d", a.calls, b.calls, c.calls)
	}
}

func TestFallbackAllFail(t *testing.T) {
	errA, errB := errors.New("a failed"), errors.New("b failed")

	err := Fallback(&mockNotifier{err: errA}, &mockNotifier{err: errB}).Send(context.Background(), Message{})
	if !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Fatalf("expected both errors, got %v", err)
	}
}

func TestFallbackStopsOnContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := notifierFunc(func(ctx context.Context, m Message) error {
		cancel()
		return ctx.Err()
	})
	b := &mockNotifier{}

	err := Fallback(a, b).Send(ctx, Message{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	if b.calls != 0 {
		t.Error("fallback must not be tried after context is done")
	}
}

func TestAsyncSendsInBackground(t *testing.T) {
	release := make(chan struct{})
	rec := &capturingNotifier{}
	slow := notifierFunc(func(ctx context.Context, m Message) error {
		<-release
		return rec.Send(ctx, m)
	})
	a := Async(slow, AsyncParams{})

	for _, s := range []string{"1", "2", "3"} {
		if err := a.Send(context.Background(), Message{Subject: s}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	close(release)

	if err := a.Close(context.Background()); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}

	sent := rec.sent()
	if len(sent) != 3 || sent[0].Subject != "1" || sent[2].Subject != "3" {
		t.Errorf("expected all messages in order after Close, got %+v", sent)
	}
}

func TestAsyncQueueFull(t *testing.T) {
	release := make(chan struct{})
	slow := notifierFunc(func(ctx context.Context, m Message) error {
		<-release
		return nil
	})
	a := Async(slow, AsyncParams{QueueSize: 1})
	defer func() {
		close(release)
		_ = a.Close(context.Background())
	}()

	// первое сообщение может уже забрать воркер, поэтому заполняем с запасом
	var err error
	for range 3 {
		if err = a.Send(context.Background(), Message{}); err != nil {
			break
		}
	}
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull, got %v", err)
	}
}

func TestAsyncClosed(t *testing.T) {
	a := Async(&mockNotifier{}, AsyncParams{})
	_ = a.Close(context.Background())
	_ = a.Close(context.Background()) // повторный Close безопасен

	if err := a.Send(context.Background(), Message{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

func TestAsyncCloseRespectsContext(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	slow := notifierFunc(func(ctx context.Context, m Message) error {
		<-release
		return nil
	})
	a := Async(slow, AsyncParams{})
	_ = a.Send(context.Background(), Message{})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if err := a.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

type ctxKey struct{}

func TestAsyncContextAndErrors(t *testing.T) {
	var gotValue any
	var gotCtxErr error
	n := notifierFunc(func(ctx context.Context, m Message) error {
		gotValue = ctx.Value(ctxKey{})
		gotCtxErr = ctx.Err()
		return errors.New("down")
	})

	var failed atomic.Int32
	a := Async(n, AsyncParams{OnError: func(m Message, err error) {
		if m.Subject == "s" && err.Error() == "down" {
			failed.Add(1)
		}
	}})

	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "v"))
	_ = a.Send(ctx, Message{Subject: "s"})
	cancel() // отмена вызывающего не должна прерывать фоновую отправку

	_ = a.Close(context.Background())

	if gotValue != "v" {
		t.Errorf("expected context values to be kept, got %v", gotValue)
	}
	if gotCtxErr != nil {
		t.Errorf("expected live context, got %v", gotCtxErr)
	}
	if failed.Load() != 1 {
		t.Errorf("expected OnError to be called once, got %d", failed.Load())
	}
}

func TestAsyncTimeout(t *testing.T) {
	var gotErr error
	n := notifierFunc(func(ctx context.Context, m Message) error {
		<-ctx.Done()
		gotErr = ctx.Err()
		return gotErr
	})
	a := Async(n, AsyncParams{Timeout: 10 * time.Millisecond})

	_ = a.Send(context.Background(), Message{})
	_ = a.Close(context.Background())

	if !errors.Is(gotErr, context.DeadlineExceeded) {
		t.Errorf("expected per-message timeout, got %v", gotErr)
	}
}

func TestLevelString(t *testing.T) {
	tests := map[Level]string{
		LevelDebug - 1: "DEBUG",
		LevelDebug:     "DEBUG",
		LevelInfo:      "INFO",
		LevelWarn:      "WARN",
		LevelError:     "ERROR",
		LevelError + 4: "ERROR",
	}
	for l, want := range tests {
		if got := l.String(); got != want {
			t.Errorf("Level(%d).String() = %q, want %q", int(l), got, want)
		}
	}
}
