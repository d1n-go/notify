package notify

import "context"

type Notifier interface {
	Send(ctx context.Context, subject string, msg string) error
}

func Send(ctx context.Context, notifiers []Notifier, subject string, msg string) error {
	for _, n := range notifiers {
		if err := n.Send(ctx, subject, msg); err != nil {
			return err
		}
	}

	return nil
}
