package notify

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"
)

// DefaultSlogTimeout limits sending of one log record when SlogHandlerOptions.Timeout is 0.
const DefaultSlogTimeout = 30 * time.Second

// SlogHandlerOptions configures NewSlogHandler.
type SlogHandlerOptions struct {
	// Level is the minimum level of records to send. Defaults to slog.LevelError.
	Level slog.Leveler

	// AddSource adds the source file and line of the log call to the message.
	AddSource bool

	// Timeout limits sending of one record. Defaults to DefaultSlogTimeout.
	// The context passed to the logger is used without its cancellation,
	// so a finished request doesn't abort the notification.
	Timeout time.Duration

	// Async sends records in the background instead of blocking the log call.
	// Use SlogHandler.Wait before exit to let pending sends finish.
	Async bool

	// OnError is called when sending a record fails. slog.Logger ignores handler
	// errors, so without it failures are silent.
	OnError func(error)

	// Subject builds the notification subject. Defaults to "LEVEL: message".
	Subject func(r slog.Record) string
}

// SlogHandler is a slog.Handler that sends log records through notifiers.
// The subject is the level and message; the body lists time, source and attributes.
//
// It is meant to be combined with a regular handler, e.g. via slog.NewMultiHandler
// (Go 1.26+), and wrapped notifiers are best throttled with Throttle.
type SlogHandler struct {
	notifiers []Notifier
	opts      SlogHandlerOptions
	wg        *sync.WaitGroup

	prefix string   // group path for following attributes, e.g. "req.user."
	attrs  []string // formatted attributes added with WithAttrs
}

func NewSlogHandler(notifiers []Notifier, opts *SlogHandlerOptions) *SlogHandler {
	h := &SlogHandler{
		notifiers: notifiers,
		wg:        &sync.WaitGroup{},
	}
	if opts != nil {
		h.opts = *opts
	}
	if h.opts.Level == nil {
		h.opts.Level = slog.LevelError
	}
	if h.opts.Timeout <= 0 {
		h.opts.Timeout = DefaultSlogTimeout
	}
	if h.opts.Subject == nil {
		h.opts.Subject = func(r slog.Record) string { return r.Level.String() + ": " + r.Message }
	}
	return h
}

func (h *SlogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.opts.Level.Level()
}

func (h *SlogHandler) Handle(ctx context.Context, r slog.Record) error {
	subject := h.opts.Subject(r)
	body := h.body(r)

	send := func() error {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), h.opts.Timeout)
		defer cancel()

		err := Send(ctx, h.notifiers, subject, body)
		if err != nil && h.opts.OnError != nil {
			h.opts.OnError(err)
		}
		return err
	}

	if h.opts.Async {
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			_ = send()
		}()
		return nil
	}
	return send()
}

// Wait blocks until all records sent in Async mode are processed.
func (h *SlogHandler) Wait() {
	h.wg.Wait()
}

func (h *SlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	h2 := *h
	h2.attrs = append([]string(nil), h.attrs...)
	for _, a := range attrs {
		h2.attrs = appendAttr(h2.attrs, h.prefix, a)
	}
	return &h2
}

func (h *SlogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	h2 := *h
	h2.prefix = h.prefix + name + "."
	return &h2
}

func (h *SlogHandler) body(r slog.Record) string {
	var lines []string
	if !r.Time.IsZero() {
		lines = append(lines, "time: "+r.Time.Format("2006-01-02 15:04:05 MST"))
	}
	if h.opts.AddSource && r.PC != 0 {
		f, _ := runtime.CallersFrames([]uintptr{r.PC}).Next()
		lines = append(lines, fmt.Sprintf("source: %s:%d", f.File, f.Line))
	}
	lines = append(lines, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		lines = appendAttr(lines, h.prefix, a)
		return true
	})
	return strings.Join(lines, "\n")
}

// appendAttr formats a as "prefix.key: value" lines, flattening groups.
func appendAttr(lines []string, prefix string, a slog.Attr) []string {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return lines
	}

	if a.Value.Kind() == slog.KindGroup {
		groupPrefix := prefix
		if a.Key != "" {
			groupPrefix += a.Key + "."
		}
		for _, ga := range a.Value.Group() {
			lines = appendAttr(lines, groupPrefix, ga)
		}
		return lines
	}

	var v string
	if a.Value.Kind() == slog.KindTime {
		v = a.Value.Time().Format(time.RFC3339)
	} else {
		v = a.Value.String()
	}
	return append(lines, prefix+a.Key+": "+v)
}
