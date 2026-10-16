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

	// DefaultTelegramMaxRetries is used when TelegramParams.MaxRetries is 0.
	DefaultTelegramMaxRetries = 3

	defaultRetryBaseDelay = time.Second
	maxRetryDelay         = 30 * time.Second
)

var defaultHTTPClient = &http.Client{Timeout: 30 * time.Second}

type TelegramParams struct {
	Token              string
	ChatID             string
	DeletePrevMessages bool
	MS                 MessageStorage

	// HTTPClient is used for Bot API requests. Defaults to a client with a 30s timeout.
	HTTPClient *http.Client

	// MaxRetries is how many times sending is retried on network errors, 5xx and
	// 429 Too Many Requests (waiting retry_after from the response, unless it is
	// over 30s). Other delays grow exponentially from 1s.
	// 0 uses DefaultTelegramMaxRetries, a negative value disables retries.
	MaxRetries int
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
	baseURL        string
	retryBaseDelay time.Duration
}

func NewTelegram(telegramParams TelegramParams) *Telegram {
	return &Telegram{
		TelegramParams: telegramParams,
		baseURL:        defaultAPIURL,
		retryBaseDelay: defaultRetryBaseDelay,
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
	Parameters  struct {
		RetryAfter int `json:"retry_after,omitempty"`
	} `json:"parameters"`
	Result struct {
		MessageID int `json:"message_id"`
	} `json:"result"`
}

// TelegramAPIError is returned when the Bot API responds with an error.
type TelegramAPIError struct {
	Method      string
	StatusCode  int    // HTTP status code
	ErrorCode   int    // error_code from the response, 0 if absent
	Description string // description from the response, empty if absent
	RetryAfter  time.Duration
}

func (e *TelegramAPIError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("telegram %s: %s (%d)", e.Method, e.Description, e.ErrorCode)
	}
	return fmt.Sprintf("telegram %s: unexpected response: %d %s", e.Method, e.StatusCode, http.StatusText(e.StatusCode))
}

// networkError marks failures to get a response at all, which are worth retrying.
type networkError struct {
	err error
}

func (e *networkError) Error() string { return "failed to send request: " + e.err.Error() }
func (e *networkError) Unwrap() error { return e.err }

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
		return nil, &networkError{err: err}
	}
	defer resp.Body.Close()

	var res TelegramResponse
	decodeErr := json.NewDecoder(resp.Body).Decode(&res)

	if resp.StatusCode != http.StatusOK || !res.Ok {
		apiErr := &TelegramAPIError{Method: method, StatusCode: resp.StatusCode}
		if decodeErr == nil {
			apiErr.ErrorCode = res.ErrorCode
			apiErr.Description = res.Description
			apiErr.RetryAfter = time.Duration(res.Parameters.RetryAfter) * time.Second
		}
		return nil, apiErr
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("failed to decode response: %w", decodeErr)
	}
	return &res, nil
}

// sendMessage sends msg, retrying transient failures. A retry after a network
// error may duplicate the message if the first request actually reached Telegram.
func (t *Telegram) sendMessage(ctx context.Context, msg string) (int, error) {
	payload := TelegramMessage{
		ChatID: t.ChatID,
		Text:   msg,
	}

	for attempt := 0; ; attempt++ {
		res, err := t.call(ctx, "sendMessage", payload)
		if err == nil {
			return res.Result.MessageID, nil
		}

		delay, ok := t.retryDelay(ctx, err, attempt)
		if !ok {
			return -1, err
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return -1, errors.Join(err, ctx.Err())
		case <-timer.C:
		}
	}
}

// retryDelay reports whether the failed attempt should be retried and how long to wait before it.
func (t *Telegram) retryDelay(ctx context.Context, err error, attempt int) (time.Duration, bool) {
	maxRetries := t.MaxRetries
	if maxRetries == 0 {
		maxRetries = DefaultTelegramMaxRetries
	}
	if attempt >= maxRetries || ctx.Err() != nil {
		return 0, false
	}

	backoff := min(t.retryBaseDelay<<attempt, maxRetryDelay)

	var apiErr *TelegramAPIError
	var netErr *networkError
	switch {
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests:
		if apiErr.RetryAfter > maxRetryDelay {
			return 0, false // long flood wait: fail now instead of blocking the caller
		}
		if apiErr.RetryAfter > 0 {
			return apiErr.RetryAfter, true
		}
		return backoff, true
	case errors.As(err, &apiErr) && apiErr.StatusCode >= 500:
		return backoff, true
	case errors.As(err, &netErr):
		return backoff, true
	default:
		return 0, false
	}
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
