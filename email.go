package notify

import (
	"context"
	"strings"

	exchangesmtp "github.com/d1n-go/exchange-smtp"
)

type EmailParams struct {
	User     string // sender address, smthg: foo@bar.com
	Login    string // optional login for auth (e.g. "DOMAIN\user" or "foo@bar.com"); derived from User if empty
	Password string
	Server   string // "host:port", e.g. "mail.bar.com:587"
	To       []string

	// Options are passed to exchangesmtp.NewMailSender (TLS settings, timeout, HELO name, etc.).
	Options []exchangesmtp.Option
}

// Username returns the login used for auth: Login if set, otherwise the part of User before "@".
// Return an empty string if both Login is empty and User doesn't have "@" symbol.
func (ep *EmailParams) Username() string {
	if ep.Login != "" {
		return ep.Login
	}
	parts := strings.SplitN(ep.User, "@", 2)
	if len(parts) != 2 {
		return ""
	}
	return parts[0]
}

type SenderEmail interface {
	SendContext(ctx context.Context, mail exchangesmtp.Mail) error
}

type Email struct {
	sender SenderEmail
	from   string
	to     []string
}

func NewEmail(ep EmailParams) *Email {
	return &Email{
		sender: exchangesmtp.NewMailSender(ep.Username(), ep.Password, ep.Server, ep.Options...),
		from:   ep.User,
		to:     ep.To,
	}
}

func (e *Email) Send(ctx context.Context, subject string, msg string) error {
	return e.sender.SendContext(ctx, exchangesmtp.Mail{
		MT:      exchangesmtp.PlainText,
		From:    e.from,
		To:      e.to,
		Subject: subject,
		Body:    msg,
	})
}
