package notify

import (
	"errors"
	"fmt"
)

// Message is a notification sent through a Notifier.
type Message struct {
	Subject string
	Body    string

	// Level is the severity. The zero value is LevelInfo.
	Level Level

	// Format tells how Body is to be interpreted. Subject is always plain text.
	Format Format

	// Silent asks to deliver the message without a sound, where supported (Telegram).
	Silent bool

	// Attachments are sent as email attachments or Telegram documents.
	Attachments []Attachment
}

// Level is the severity of a message. Values match log/slog levels,
// so a slog.Level converts with Level(l).
type Level int

const (
	LevelDebug Level = -4
	LevelInfo  Level = 0
	LevelWarn  Level = 4
	LevelError Level = 8
)

func (l Level) String() string {
	switch {
	case l >= LevelError:
		return "ERROR"
	case l >= LevelWarn:
		return "WARN"
	case l >= LevelInfo:
		return "INFO"
	default:
		return "DEBUG"
	}
}

// Format of a message body.
type Format int

const (
	// FormatText is plain text.
	FormatText Format = iota

	// FormatHTML is HTML. For Telegram only its subset is allowed: <b>, <i>, <u>,
	// <s>, <a href>, <code>, <pre>, <blockquote>; line breaks are "\n", not <br>.
	FormatHTML
)

// Attachment is a file attached to a message.
type Attachment struct {
	Name        string // file name shown to the recipient
	ContentType string // e.g. "text/csv"; optional
	Data        []byte
}

// ErrEmptyAttachment is returned when an attachment has no data.
var ErrEmptyAttachment = errors.New("notify: attachment has no data")

func validateAttachments(as []Attachment) error {
	for _, a := range as {
		if len(a.Data) == 0 {
			return fmt.Errorf("%w: %q", ErrEmptyAttachment, a.Name)
		}
	}
	return nil
}
