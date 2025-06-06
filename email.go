package notify

import (
	"context"
	"github.com/demen1n/exchange-smtp"
	"strings"
)

type EmailParams struct {
	User     string // smthg: foo@bar.com
	Password string
	Server   string
	To       []string
}

// Username uses User field to get username for login. Return an empty string if User field is empty or doesn't have "@" symbol.
func (ep *EmailParams) Username() string {
	parts := strings.SplitN(ep.User, "@", 2)
	if len(parts) != 2 {
		return ""
	}
	return parts[0]
}

type SenderEmail interface {
	Send(subject, body string) error
}

type Email struct {
	sender SenderEmail
}

func NewEmail(ep EmailParams) *Email {
	return &Email{sender: exchangesmtp.NewQuickSender(ep.Username(), ep.Password, ep.Server, ep.User, ep.To)}
}

func (e *Email) Send(ctx context.Context, subject string, msg string) error {
	// TODO: в exchange-smtp начать использовать контекст
	result := make(chan error, 1)

	go func() {
		err := e.sender.Send(subject, msg)
		result <- err
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-result:
		return err
	}
}
