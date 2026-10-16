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
	Send(ctx context.Context, subject string, msg string) error
}
```

`notify.Send` sends the message through every notifier, even if some of them fail, and returns all errors joined with `errors.Join`:

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

## Email

Sends a plain text email from `User` to `To`.

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

Sends `[subject]\n\nmsg` as plain text. Text longer than the Bot API limit (4096 characters) is truncated.

- `HTTPClient` — client for Bot API requests. Defaults to a client with a 30 second timeout.
- `MaxRetries` — retries on network errors, 5xx and `429 Too Many Requests` (default 3, negative disables). For 429 the `retry_after` from Telegram is respected; if it is longer than 30 seconds, the error is returned right away. Other delays grow exponentially from 1 second. Note that a retry after a network error may duplicate the message if the first request actually reached Telegram.
- `DeletePrevMessages` + `MS` — keep only the latest notification in the chat: before sending, the previous message is deleted, and the new message ID is saved. `MS` is your implementation of `MessageStorage` (a file, a database row, etc.); `GetMessageID` should return `0` when nothing is stored yet. Errors from deleting or storing are ignored, so they never block the notification itself.

```go
type MessageStorage interface {
	StoreMessageID(messageID int) error
	GetMessageID() (int, error)
}
```

Errors returned by the Telegram notifier never contain the bot token. Bot API errors are returned as `*notify.TelegramAPIError` with the status code, description and `retry_after`.

## Throttling

`notify.Throttle` wraps any notifier so that the same message is sent at most once per window. This keeps a chat from being flooded when something fails in a loop:

```go
tg := notify.Throttle(notify.NewTelegram(params), notify.ThrottleParams{
	Window: 10 * time.Minute,
})
```

Suppressed messages are dropped (`Send` returns `nil`), and their count is appended to the next message that gets through: `(similar messages suppressed: 42)`. A failed send doesn't count, so the next attempt goes through immediately.

By default messages are grouped by subject and text. Use `Key` to group differently:

```go
// alerts whose text contains changing details: group by subject only
Key: func(subject, msg string) string { return subject },

// at most one message per window overall
Key: func(subject, msg string) string { return "" },
```

## Logging with slog

`notify.NewSlogHandler` is a `slog.Handler` that sends log records (errors by default) through notifiers. The subject is `LEVEL: message`, the body lists time, source and attributes:

```go
notifyHandler := notify.NewSlogHandler(
	[]notify.Notifier{notify.Throttle(tg, notify.ThrottleParams{Window: 10 * time.Minute})},
	&notify.SlogHandlerOptions{
		Level:     slog.LevelError,
		AddSource: true,
		Async:     true,
		OnError:   func(err error) { fmt.Fprintln(os.Stderr, "notify:", err) },
	},
)
defer notifyHandler.Wait() // let async sends finish before exit

// Go 1.26+: log to stdout and send errors to Telegram
logger := slog.New(slog.NewMultiHandler(slog.NewTextHandler(os.Stdout, nil), notifyHandler))
logger.Error("payment failed", "order", 42, "err", err)
```

Options:

- `Level` — minimum level to send, `slog.LevelError` by default.
- `AddSource` — add file and line of the log call.
- `Timeout` — limit for sending one record, 30 seconds by default. Cancellation of the context passed to the logger is ignored, so a finished request doesn't abort the notification.
- `Async` — send in the background instead of blocking the log call; call `Wait` before exit.
- `OnError` — called when sending fails (`slog.Logger` ignores handler errors).
- `Subject` — custom subject builder.

Throttling the notifiers is recommended: an error logged in a loop otherwise turns into a flood of messages.
