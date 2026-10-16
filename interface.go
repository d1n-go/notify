package notify

import (
	"context"
	"errors"
)

type Notifier interface {
	Send(ctx context.Context, subject string, msg string) error
}

// Send sends the message through every notifier, even if some of them fail.
// It returns all errors joined with errors.Join, or nil if every send succeeded.
func Send(ctx context.Context, notifiers []Notifier, subject string, msg string) error {
	var errs []error
	for _, n := range notifiers {
		if err := n.Send(ctx, subject, msg); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
