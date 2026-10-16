package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
	"unicode/utf16"
)

const (
	defaultAPIURL = "https://api.telegram.org/bot"

	// telegramMaxMessageLen is the Bot API limit for message text, in UTF-16 code units.
	telegramMaxMessageLen = 4096
)

var defaultHTTPClient = &http.Client{Timeout: 30 * time.Second}

type TelegramParams struct {
	Token              string
	ChatID             string
	DeletePrevMessages bool
	MS                 MessageStorage

	// HTTPClient is used for Bot API requests. Defaults to a client with a 30s timeout.
	HTTPClient *http.Client
}

// MessageStorage keeps the ID of the last sent message so it can be deleted
// before the next one when DeletePrevMessages is set.
// GetMessageID should return 0 (or a negative value) when nothing is stored yet.
type MessageStorage interface {
	StoreMessageID(messageID int) error
	GetMessageID() (int, error)
}

type Telegram struct {
	TelegramParams
	baseURL string
}

func NewTelegram(telegramParams TelegramParams) *Telegram {
	return &Telegram{
		TelegramParams: telegramParams,
		baseURL:        defaultAPIURL,
	}
}

type TelegramMessage struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

type TelegramResponse struct {
	Ok          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code,omitempty"`
	Description string `json:"description,omitempty"`
	Result      struct {
		MessageID int `json:"message_id"`
	} `json:"result"`
}

// Send sends "[subject]\n\nmsg" to the chat. Text over the Bot API limit is truncated.
// Failures to delete the previous message or to store the new message ID are ignored.
func (t *Telegram) Send(ctx context.Context, subject string, msg string) error {
	if t.DeletePrevMessages && t.MS != nil {
		if prevID, err := t.MS.GetMessageID(); err == nil && prevID > 0 {
			_ = t.deleteMessage(ctx, prevID)
		}
	}

	msgID, err := t.sendMessage(ctx, truncateUTF16(fmt.Sprintf("[%s]\n\n%s", subject, msg), telegramMaxMessageLen))
	if err != nil {
		return err
	}

	if t.DeletePrevMessages && t.MS != nil {
		_ = t.MS.StoreMessageID(msgID)
	}
	return nil
}

// call posts payload to the Bot API method and decodes the response.
// The bot token is never included in returned errors.
func (t *Telegram) call(ctx context.Context, method string, payload any) (*TelegramResponse, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+t.Token+"/"+method, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create request for %s", method)
	}
	req.Header.Set("Content-Type", "application/json")

	client := t.HTTPClient
	if client == nil {
		client = defaultHTTPClient
	}

	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			// url.Error.Error() contains the full URL with the bot token
			ue.URL = t.baseURL + "<token>/" + method
		}
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	var res TelegramResponse
	decodeErr := json.NewDecoder(resp.Body).Decode(&res)

	if resp.StatusCode != http.StatusOK || !res.Ok {
		if decodeErr == nil && res.Description != "" {
			return nil, fmt.Errorf("telegram %s: %s (%d)", method, res.Description, res.ErrorCode)
		}
		return nil, fmt.Errorf("telegram %s: unexpected response: %s", method, resp.Status)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("failed to decode response: %w", decodeErr)
	}
	return &res, nil
}

func (t *Telegram) sendMessage(ctx context.Context, msg string) (int, error) {
	res, err := t.call(ctx, "sendMessage", TelegramMessage{
		ChatID: t.ChatID,
		Text:   msg,
	})
	if err != nil {
		return -1, err
	}
	return res.Result.MessageID, nil
}

func (t *Telegram) deleteMessage(ctx context.Context, messageID int) error {
	_, err := t.call(ctx, "deleteMessage", map[string]any{
		"chat_id":    t.ChatID,
		"message_id": messageID,
	})
	return err
}

// truncateUTF16 cuts s to at most limit UTF-16 code units, ending with "…" if cut.
func truncateUTF16(s string, limit int) string {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	if n <= limit {
		return s
	}

	const ellipsis = '…'
	limit -= utf16.RuneLen(ellipsis)
	n = 0
	for i, r := range s {
		n += utf16.RuneLen(r)
		if n > limit {
			return s[:i] + string(ellipsis)
		}
	}
	return s
}
