package notify

import (
	"context"
	"errors"
	"sync"
	"time"
)

type multi []Notifier

// Multi returns a Notifier that sends every message through all notifiers in
// parallel. It waits for all of them and returns their errors joined with
// errors.Join (in the order of notifiers), or nil if every send succeeded.
func Multi(notifiers ...Notifier) Notifier {
	return multi(append([]Notifier(nil), notifiers...))
}

func (ns multi) Send(ctx context.Context, m Message) error {
	switch len(ns) {
	case 0:
		return nil
	case 1:
		return ns[0].Send(ctx, m)
	}

	errs := make([]error, len(ns))
	var wg sync.WaitGroup
	for i, n := range ns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = n.Send(ctx, m)
		}()
	}
	wg.Wait()

	return errors.Join(errs...)
}

type fallback []Notifier

// Fallback returns a Notifier that tries notifiers in order and stops at the
// first one that succeeds, e.g. Fallback(telegram, email). If all of them fail,
// their errors are returned joined with errors.Join.
func Fallback(notifiers ...Notifier) Notifier {
	return fallback(append([]Notifier(nil), notifiers...))
}

func (ns fallback) Send(ctx context.Context, m Message) error {
	var errs []error
	for _, n := range ns {
		err := n.Send(ctx, m)
		if err == nil {
			return nil
		}
		errs = append(errs, err)

		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(errs...)
}

const (
	defaultAsyncQueueSize = 100
	defaultAsyncTimeout   = 30 * time.Second
)

var (
	// ErrQueueFull is returned by AsyncNotifier.Send when the queue has no room.
	ErrQueueFull = errors.New("notify: queue is full")

	// ErrClosed is returned by AsyncNotifier.Send after Close.
	ErrClosed = errors.New("notify: notifier is closed")
)

// AsyncParams configures Async.
type AsyncParams struct {
	// QueueSize is the number of messages waiting to be sent. Defaults to 100.
	QueueSize int

	// Timeout limits sending of one message. Defaults to 30s.
	Timeout time.Duration

	// OnError is called from the background goroutine when sending fails.
	OnError func(m Message, err error)
}

// AsyncNotifier queues messages and sends them in a background goroutine, see Async.
type AsyncNotifier struct {
	next    Notifier
	timeout time.Duration
	onError func(Message, error)

	mu     sync.RWMutex
	closed bool
	queue  chan asyncItem
	done   chan struct{}
}

type asyncItem struct {
	ctx context.Context
	m   Message
}

// Async wraps n so that Send only puts the message in a queue and returns at
// once; messages are sent one by one in a background goroutine. Send returns
// ErrQueueFull instead of blocking when the queue is full.
//
// Cancellation of the context passed to Send is ignored (its values are kept),
// each message gets its own Timeout. Call Close to send what is queued and
// stop the goroutine. The message, including attachment data, must not be
// modified after Send.
func Async(n Notifier, p AsyncParams) *AsyncNotifier {
	size := p.QueueSize
	if size <= 0 {
		size = defaultAsyncQueueSize
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultAsyncTimeout
	}

	a := &AsyncNotifier{
		next:    n,
		timeout: timeout,
		onError: p.OnError,
		queue:   make(chan asyncItem, size),
		done:    make(chan struct{}),
	}
	go a.run()
	return a
}

func (a *AsyncNotifier) Send(ctx context.Context, m Message) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return ErrClosed
	}
	select {
	case a.queue <- asyncItem{ctx: context.WithoutCancel(ctx), m: m}:
		return nil
	default:
		return ErrQueueFull
	}
}

// Close stops accepting messages and waits until the queued ones are sent or ctx is done.
// It is safe to call Close more than once.
func (a *AsyncNotifier) Close(ctx context.Context) error {
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		close(a.queue)
	}
	a.mu.Unlock()

	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *AsyncNotifier) run() {
	defer close(a.done)

	for it := range a.queue {
		ctx, cancel := context.WithTimeout(it.ctx, a.timeout)
		err := a.next.Send(ctx, it.m)
		cancel()

		if err != nil && a.onError != nil {
			a.onError(it.m, err)
		}
	}
}
