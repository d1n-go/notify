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

// Send sends m as an email. Warnings and errors get a "[WARN]" or "[ERROR]"
// subject prefix, errors are also marked with "Importance: High".
// If Body is empty, Subject is used as the body.
func (e *Email) Send(ctx context.Context, m Message) error {
	if err := validateAttachments(m.Attachments); err != nil {
		return err
	}

	mail := exchangesmtp.Mail{
		MT:      exchangesmtp.PlainText,
		From:    e.from,
		To:      e.to,
		Subject: m.Subject,
		Body:    m.Body,
	}
	if m.Format == FormatHTML {
		mail.MT = exchangesmtp.HTML
	}
	if mail.Body == "" {
		mail.Body = m.Subject
	}
	if m.Level >= LevelWarn {
		mail.Subject = "[" + m.Level.String() + "] " + mail.Subject
	}
	if m.Level >= LevelError {
		mail.Headers = map[string]string{"Importance": "High"}
	}
	for _, a := range m.Attachments {
		mail.Attachment = append(mail.Attachment, exchangesmtp.AttachmentFile{
			Name:        a.Name,
			ContentType: a.ContentType,
			Body:        a.Data,
		})
	}

	return e.sender.SendContext(ctx, mail)
}
