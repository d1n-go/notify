package notify

import (
	"context"
	"errors"
	"testing"
	"time"
)

type mockSender struct {
	sendFunc func(subject, msg string) error
}

func (m *mockSender) Send(subject, msg string) error {
	return m.sendFunc(subject, msg)
}

func newTestEmail(sendFunc func(subject, msg string) error) *Email {
	return &Email{
		sender: &mockSender{sendFunc: sendFunc},
	}
}

func TestEmailSendSuccess(t *testing.T) {
	email := newTestEmail(func(subject, msg string) error {
		return nil // имитируем успешную отправку
	})

	ctx := context.Background()
	err := email.Send(ctx, "Test", "Hello")
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestEmailSendFail(t *testing.T) {
	email := newTestEmail(func(subject, msg string) error {
		return errors.New("smtp failed")
	})

	ctx := context.Background()
	err := email.Send(ctx, "Test", "Hello")
	if err == nil || err.Error() != "smtp failed" {
		t.Fatalf("expected smtp failed error, got %v", err)
	}
}

func TestEmailSendContextTimeout(t *testing.T) {
	email := newTestEmail(func(subject, msg string) error {
		time.Sleep(100 * time.Millisecond) // имитируем "зависание"
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := email.Send(ctx, "Test", "Hello")

	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}

	if time.Since(start) > 50*time.Millisecond {
		t.Fatalf("ыend took too long, context may not have canceled in time")
	}
}
