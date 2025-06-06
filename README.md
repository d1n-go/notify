# Notify

This library provides ability to send notifications using multiple services:

- Email (via MS Exchange)
- Telegram

## Install

`go get -u github.com/demen1n/notify`

## Usage

All supported notification methods could adhere to the following interface. Example on how to use it:

```go
package main

import (
	"context"
	"fmt"

	"github.com/demen1n/notify"
)

func main() {
	notifiers := []notify.Notifier{
		notify.NewEmail(notify.EmailParams{}),
		notify.NewTelegram(notify.TelegramParams{)),
	}
	err = notify.Send(context.Background(), notifiers, "Hello from subject", "Hello, world!")
	if err != nil {
		fmt.Printf("Sent message error: %s", err))
	}
}