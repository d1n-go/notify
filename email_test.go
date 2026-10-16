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

	err := email.Send(context.Background(), Message{Subject: "Test", Body: "Hello"})
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

	err := email.Send(context.Background(), Message{Subject: "Test", Body: "Hello"})
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

	err := email.Send(ctx, Message{Subject: "Test", Body: "Hello"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
}

func TestEmailSendMessageOptions(t *testing.T) {
	var got exchangesmtp.Mail
	email := newTestEmail(func(ctx context.Context, mail exchangesmtp.Mail) error {
		got = mail
		return nil
	})

	err := email.Send(context.Background(), Message{
		Subject:     "Disk full",
		Body:        "<b>/var</b> is 99% full",
		Level:       LevelError,
		Format:      FormatHTML,
		Attachments: []Attachment{{Name: "df.txt", ContentType: "text/plain", Data: []byte("data")}},
	})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}

	if got.Subject != "[ERROR] Disk full" {
		t.Errorf("unexpected subject %q", got.Subject)
	}
	if got.MT != exchangesmtp.HTML {
		t.Errorf("expected HTML mail, got %v", got.MT)
	}
	if got.Headers["Importance"] != "High" {
		t.Errorf("expected high importance for errors, got headers %v", got.Headers)
	}
	if len(got.Attachment) != 1 || got.Attachment[0].Name != "df.txt" || string(got.Attachment[0].Body) != "data" {
		t.Errorf("unexpected attachments: %+v", got.Attachment)
	}
}

func TestEmailSendLevels(t *testing.T) {
	tests := []struct {
		level       Level
		wantSubject string
		wantHigh    bool
	}{
		{LevelDebug, "s", false},
		{LevelInfo, "s", false},
		{LevelWarn, "[WARN] s", false},
		{LevelError, "[ERROR] s", true},
	}
	for _, tt := range tests {
		t.Run(tt.level.String(), func(t *testing.T) {
			var got exchangesmtp.Mail
			email := newTestEmail(func(ctx context.Context, mail exchangesmtp.Mail) error {
				got = mail
				return nil
			})

			_ = email.Send(context.Background(), Message{Subject: "s", Body: "b", Level: tt.level})

			if got.Subject != tt.wantSubject {
				t.Errorf("subject = %q, want %q", got.Subject, tt.wantSubject)
			}
			if (got.Headers["Importance"] == "High") != tt.wantHigh {
				t.Errorf("unexpected headers %v", got.Headers)
			}
		})
	}
}

func TestEmailSendEmptyBodyUsesSubject(t *testing.T) {
	var got exchangesmtp.Mail
	email := newTestEmail(func(ctx context.Context, mail exchangesmtp.Mail) error {
		got = mail
		return nil
	})

	_ = email.Send(context.Background(), Message{Subject: "Backup done"})

	if got.Body != "Backup done" {
		t.Errorf("expected subject as body, got %q", got.Body)
	}
}

func TestEmailSendRejectsEmptyAttachment(t *testing.T) {
	called := false
	email := newTestEmail(func(ctx context.Context, mail exchangesmtp.Mail) error {
		called = true
		return nil
	})

	// без проверки exchange-smtp прочитал бы файл с диска по имени
	err := email.Send(context.Background(), Message{Subject: "s", Body: "b", Attachments: []Attachment{{Name: "/etc/passwd"}}})
	if !errors.Is(err, ErrEmptyAttachment) {
		t.Fatalf("expected ErrEmptyAttachment, got %v", err)
	}
	if called {
		t.Error("mail must not be sent with an empty attachment")
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
