package notify

import (
	"context"
	"errors"
	"testing"
)

type mockNotifier struct {
	err   error
	calls int
}

func (m *mockNotifier) Send(ctx context.Context, subject string, msg string) error {
	m.calls++
	return m.err
}

func TestSendAllSucceed(t *testing.T) {
	a, b := &mockNotifier{}, &mockNotifier{}

	if err := Send(context.Background(), []Notifier{a, b}, "s", "m"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Errorf("expected each notifier to be called once, got %d and %d", a.calls, b.calls)
	}
}

func TestSendContinuesAfterError(t *testing.T) {
	errA, errC := errors.New("a failed"), errors.New("c failed")
	a, b, c := &mockNotifier{err: errA}, &mockNotifier{}, &mockNotifier{err: errC}

	err := Send(context.Background(), []Notifier{a, b, c}, "s", "m")
	if !errors.Is(err, errA) || !errors.Is(err, errC) {
		t.Fatalf("expected both errors joined, got %v", err)
	}
	if b.calls != 1 || c.calls != 1 {
		t.Errorf("expected notifiers after a failure to be called, got %d and %d", b.calls, c.calls)
	}
}

func TestSendNoNotifiers(t *testing.T) {
	if err := Send(context.Background(), nil, "s", "m"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}
