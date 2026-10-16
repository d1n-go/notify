package notify

import (
	"context"
	"errors"
	"testing"
	"time"

	exchangesmtp "github.com/d1n-go/exchange-smtp"
)

type mockSender struct {
	sendFunc func(ctx context.Context, mail exchangesmtp.Mail) error
}

func (m *mockSender) SendContext(ctx context.Context, mail exchangesmtp.Mail) error {
	return m.sendFunc(ctx, mail)
}

func newTestEmail(sendFunc func(ctx context.Context, mail exchangesmtp.Mail) error) *Email {
	return &Email{
		sender: &mockSender{sendFunc: sendFunc},
		from:   "foo@bar.com",
		to:     []string{"baz@bar.com"},
	}
}

func TestEmailSendSuccess(t *testing.T) {
	var got exchangesmtp.Mail
	email := newTestEmail(func(ctx context.Context, mail exchangesmtp.Mail) error {
		got = mail
		return nil // имитируем успешную отправку
	})

	err := email.Send(context.Background(), "Test", "Hello")
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}

	if got.From != "foo@bar.com" || len(got.To) != 1 || got.To[0] != "baz@bar.com" ||
		got.Subject != "Test" || got.Body != "Hello" || got.MT != exchangesmtp.PlainText {
		t.Errorf("unexpected mail: %+v", got)
	}
}

func TestEmailSendFail(t *testing.T) {
	email := newTestEmail(func(ctx context.Context, mail exchangesmtp.Mail) error {
		return errors.New("smtp failed")
	})

	err := email.Send(context.Background(), "Test", "Hello")
	if err == nil || err.Error() != "smtp failed" {
		t.Fatalf("expected smtp failed error, got %v", err)
	}
}

func TestEmailSendPassesContext(t *testing.T) {
	email := newTestEmail(func(ctx context.Context, mail exchangesmtp.Mail) error {
		select {
		case <-ctx.Done(): // имитируем прерывание SMTP-сессии
			return ctx.Err()
		case <-time.After(time.Second):
			return nil
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := email.Send(ctx, "Test", "Hello")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
}

func TestEmailParamsUsername(t *testing.T) {
	tests := []struct {
		name string
		ep   EmailParams
		want string
	}{
		{"from user", EmailParams{User: "foo@bar.com"}, "foo"},
		{"no at", EmailParams{User: "foo"}, ""},
		{"empty", EmailParams{}, ""},
		{"login overrides", EmailParams{User: "foo@bar.com", Login: `CORP\foo`}, `CORP\foo`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ep.Username(); got != tt.want {
				t.Errorf("Username() = %q, want %q", got, tt.want)
			}
		})
	}
}
