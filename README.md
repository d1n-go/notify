# Notify

This library provides ability to send notifications using multiple services:

- Email (via MS Exchange, using [exchange-smtp](https://github.com/d1n-go/exchange-smtp))
- Telegram

## Install

`go get -u github.com/d1n-go/notify`

## Usage

All supported notification methods adhere to the `Notifier` interface:

```go
type Notifier interface {
	Send(ctx context.Context, m notify.Message) error
}
```

`notify.Send` is a shortcut for a plain text message: it sends it through every notifier in parallel, even if some of them fail, and returns all errors joined with `errors.Join`:

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/d1n-go/notify"
)

func main() {
	notifiers := []notify.Notifier{
		notify.NewEmail(notify.EmailParams{
			User:     "foo@bar.com",
			Password: "secret",
			Server:   "mail.bar.com:587",
			To:       []string{"baz@bar.com"},
		}),
		notify.NewTelegram(notify.TelegramParams{
			Token:  "bot-token",
			ChatID: "chat-id",
		}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := notify.Send(ctx, notifiers, "Hello from subject", "Hello, world!")
	if err != nil {
		fmt.Printf("Sent message error: %s\n", err)
	}
}
```

Cancelling the context aborts sending for both email (the SMTP session is closed) and Telegram.

## Message

```go
err := notifier.Send(ctx, notify.Message{
	Subject: "Nightly report",
	Body:    "<b>3</b> jobs failed, see the attached log",
	Level:   notify.LevelError,
	Format:  notify.FormatHTML,
	Silent:  false,
	Attachments: []notify.Attachment{
		{Name: "jobs.log", ContentType: "text/plain", Data: logData},
	},
})
```

- `Level` — `LevelDebug`, `LevelInfo` (zero value), `LevelWarn`, `LevelError`. Values match `log/slog`, so `notify.Level(slog.LevelWarn)` works. Email adds a `[WARN]`/`[ERROR]` subject prefix and marks errors with `Importance: High`; Telegram prefixes the message with ⚠️/🔴.
- `Format` — `FormatText` (default) or `FormatHTML`. Telegram supports only a subset of HTML: `<b>`, `<i>`, `<u>`, `<s>`, `<a href>`, `<code>`, `<pre>`, `<blockquote>`, and line breaks are `\n`, not `<br>`. Stick to it if the message goes to both email and Telegram.
- `Silent` — deliver without a sound (Telegram).
- `Attachments` — email attachments or Telegram documents (sent as replies to the message). An attachment without `Data` is rejected with `ErrEmptyAttachment`.

## Combining notifiers

All combinators are `Notifier`s themselves, so they can be nested:

```go
// send to both channels in parallel; errors are joined
both := notify.Multi(tg, email)

// try Telegram, use email only if it fails
reliable := notify.Fallback(tg, email)

// don't block the caller: queue messages and send them in the background
async := notify.Async(notify.Fallback(tg, email), notify.AsyncParams{
	QueueSize: 100,              // default 100
	Timeout:   30 * time.Second, // per message, default 30s
	OnError:   func(m notify.Message, err error) { log.Printf("notify %q: %v", m.Subject, err) },
})
defer async.Close(context.Background()) // sends what is queued
```

- `Multi` waits for all notifiers and returns their errors joined with `errors.Join`, in the order of notifiers.
- `Fallback` returns `nil` as soon as one notifier succeeds; if all fail, their errors are joined. It stops early if the context is done.
- `Async.Send` returns `ErrQueueFull` instead of blocking when the queue is full, and `ErrClosed` after `Close`. Cancellation of the caller's context is ignored (its values are kept). Don't modify a message, including attachment data, after passing it to `Send`.

## Email

Sends an email from `User` to `To`. If the message has no body, the subject is used as the body.

- `Login` — login for auth. If empty, the part of `User` before `@` is used. Set it explicitly when the server expects `DOMAIN\user` or the full address.
- `Options` — [exchange-smtp options](https://github.com/d1n-go/exchange-smtp#options), e.g. for old on-premises Exchange:

```go
notify.NewEmail(notify.EmailParams{
	User:     "reports@corp.local",
	Login:    `CORP\reports`,
	Password: "secret",
	Server:   "mail.corp.local:587",
	To:       []string{"admin@corp.local"},
	Options: []exchangesmtp.Option{
		exchangesmtp.WithInsecureSkipVerify(),
		exchangesmtp.WithHelloName("app.corp.local"),
	},
})
```

## Telegram

Sends `[subject]\n\nbody` (with ⚠️/🔴 in front for warnings and errors). Text longer than the Bot API limit (4096 characters) is truncated; HTML is then sent as plain text with tags stripped, since cutting it could break the markup.

- `MessageThreadID` — send to a topic of a forum supergroup.
- `HTTPClient` — client for Bot API requests. Defaults to a client with a 30 second timeout.
- `MaxRetries` — retries on network errors, 5xx and `429 Too Many Requests` (default 3, negative disables). For 429 the `retry_after` from Telegram is respected; if it is longer than 30 seconds, the error is returned right away. Other delays grow exponentially from 1 second. Note that a retry after a network error may duplicate the message if the first request actually reached Telegram.
- `DeletePrevMessages` + `MS` — keep only the latest notification in the chat: before sending, the previous message is deleted, and the new message ID is saved. `MS` is your implementation of `MessageStorage` (a file, a database row, etc.); `GetMessageID` should return `0` when nothing is stored yet. Errors from deleting or storing are ignored, so they never block the notification itself.

```go
type MessageStorage interface {
	StoreMessageID(messageID int) error
	GetMessageID() (int, error)
}
```

Only the text message is deleted: attachments sent with it stay in the chat.

Errors returned by the Telegram notifier never contain the bot token. Bot API errors are returned as `*notify.TelegramAPIError` with the status code, description and `retry_after`.

## Throttling

`notify.Throttle` wraps any notifier so that the same message is sent at most once per window. This keeps a chat from being flooded when something fails in a loop:

```go
tg := notify.Throttle(notify.NewTelegram(params), notify.ThrottleParams{
	Window: 10 * time.Minute,
})
```

Suppressed messages are dropped (`Send` returns `nil`), and their count is appended to the next message that gets through: `(similar messages suppressed: 42)`. A failed send doesn't count, so the next attempt goes through immediately.

By default messages are grouped by subject and body. Use `Key` to group differently:

```go
// alerts whose text contains changing details: group by subject only
Key: func(m notify.Message) string { return m.Subject },

// at most one message per window overall
Key: func(m notify.Message) string { return "" },
```

## Logging with slog

`notify.NewSlogHandler` is a `slog.Handler` that sends log records (errors by default) through notifiers. The subject is the log message, the record level becomes `Message.Level` (so email gets `[ERROR]` and Telegram 🔴), and the body lists time, source and attributes:

```go
// send in the background so logging never blocks on the network
alerts := notify.Async(
	notify.Throttle(tg, notify.ThrottleParams{Window: 10 * time.Minute}),
	notify.AsyncParams{
		OnError: func(m notify.Message, err error) { fmt.Fprintln(os.Stderr, "notify:", err) },
	},
)
defer alerts.Close(context.Background()) // let queued alerts go out before exit

notifyHandler := notify.NewSlogHandler([]notify.Notifier{alerts}, &notify.SlogHandlerOptions{
	Level:     slog.LevelError,
	AddSource: true,
	OnError:   func(err error) { fmt.Fprintln(os.Stderr, "notify:", err) }, // e.g. ErrQueueFull
})

// Go 1.26+: log to stdout and send errors to Telegram
logger := slog.New(slog.NewMultiHandler(slog.NewTextHandler(os.Stdout, nil), notifyHandler))
logger.Error("payment failed", "order", 42, "err", err)
```

Options:

- `Level` — minimum level to send, `slog.LevelError` by default.
- `AddSource` — add file and line of the log call.
- `Timeout` — limit for sending one record, 30 seconds by default. Cancellation of the context passed to the logger is ignored, so a finished request doesn't abort the notification.
- `OnError` — called when sending fails (`slog.Logger` ignores handler errors). With `notify.Async` this only sees enqueue errors such as `ErrQueueFull`; send errors go to `AsyncParams.OnError`.
- `Async` — deprecated: wrap the notifier with `notify.Async` as shown above. Unlike it, this option starts a goroutine per record with no limit, and `Wait` has no timeout.
- `Subject` — custom subject builder, the log message by default.

Throttling the notifiers is recommended: an error logged in a loop otherwise turns into a flood of messages.
