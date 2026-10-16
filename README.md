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
- `DeletePrevMessages` + `MS` — keep only the latest notification in the chat: before sending, the previous message is deleted, and the new message ID is saved. `MS` is your implementation of `MessageStorage` (a file, a database row, etc.); `GetMessageID` should return `0` when nothing is stored yet. Errors from deleting or storing are ignored, so they never block the notification itself.

```go
type MessageStorage interface {
	StoreMessageID(messageID int) error
	GetMessageID() (int, error)
}
```

Errors returned by the Telegram notifier never contain the bot token.
