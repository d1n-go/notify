package notify

import (
	"context"
)

type Notifier interface {
	Send(ctx context.Context, m Message) error
}

// Send sends a plain text message with the given subject through every notifier.
// It is a shortcut for Multi(notifiers...).Send(ctx, Message{Subject: subject, Body: msg}).
func Send(ctx context.Context, notifiers []Notifier, subject string, msg string) error {
	return Multi(notifiers...).Send(ctx, Message{Subject: subject, Body: msg})
}
