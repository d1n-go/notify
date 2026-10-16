package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
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
	Token  string
	ChatID string

	// MessageThreadID sends messages to a topic of a forum supergroup.
	MessageThreadID int

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
	ChatID              string `json:"chat_id"`
	MessageThreadID     int    `json:"message_thread_id,omitempty"`
	Text                string `json:"text"`
	ParseMode           string `json:"parse_mode,omitempty"`
	DisableNotification bool   `json:"disable_notification,omitempty"`
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

// Send sends m to the chat as "<level emoji> [subject]\n\nbody", with ⚠️ for
// warnings and 🔴 for errors. Text over the Bot API limit is truncated (HTML is
// then sent as plain text, since cutting it could break tags).
// Attachments are sent as documents replying to the message.
// Failures to delete the previous message or to store the new message ID are ignored.
func (t *Telegram) Send(ctx context.Context, m Message) error {
	if err := validateAttachments(m.Attachments); err != nil {
		return err
	}

	if t.DeletePrevMessages && t.MS != nil {
		if prevID, err := t.MS.GetMessageID(); err == nil && prevID > 0 {
			_ = t.deleteMessage(ctx, prevID)
		}
	}

	text, parseMode := telegramText(m)
	msgID, err := t.sendMessage(ctx, TelegramMessage{
		ChatID:              t.ChatID,
		MessageThreadID:     t.MessageThreadID,
		Text:                text,
		ParseMode:           parseMode,
		DisableNotification: m.Silent,
	})
	if err != nil {
		return err
	}

	if t.DeletePrevMessages && t.MS != nil {
		_ = t.MS.StoreMessageID(msgID)
	}

	for _, a := range m.Attachments {
		if err := t.sendDocument(ctx, a, msgID, m.Silent); err != nil {
			return fmt.Errorf("attachment %q: %w", a.Name, err)
		}
	}
	return nil
}

var htmlTag = regexp.MustCompile(`<[^>]*>`)

// telegramText builds the message text and its parse_mode.
func telegramText(m Message) (string, string) {
	var emoji string
	switch {
	case m.Level >= LevelError:
		emoji = "🔴"
	case m.Level >= LevelWarn:
		emoji = "⚠️"
	}

	build := func(subject, body string) string {
		var head []string
		if emoji != "" {
			head = append(head, emoji)
		}
		if subject != "" {
			head = append(head, "["+subject+"]")
		}

		var parts []string
		if len(head) > 0 {
			parts = append(parts, strings.Join(head, " "))
		}
		if body != "" {
			parts = append(parts, body)
		}
		return strings.Join(parts, "\n\n")
	}

	if m.Format == FormatHTML {
		text := build(html.EscapeString(m.Subject), m.Body)
		if utf16Len(text) <= telegramMaxMessageLen {
			return text, "HTML"
		}
		m.Body = html.UnescapeString(htmlTag.ReplaceAllString(m.Body, ""))
	}
	return truncateUTF16(build(m.Subject, m.Body), telegramMaxMessageLen), ""
}

// call posts body to the Bot API method and decodes the response.
// The bot token is never included in returned errors.
func (t *Telegram) call(ctx context.Context, method, contentType string, body []byte) (*TelegramResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+t.Token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request for %s", method)
	}
	req.Header.Set("Content-Type", contentType)

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

// callWithRetry is call with retries of transient failures. A retry after a network
// error may duplicate the message if the first request actually reached Telegram.
func (t *Telegram) callWithRetry(ctx context.Context, method, contentType string, body []byte) (*TelegramResponse, error) {
	for attempt := 0; ; attempt++ {
		res, err := t.call(ctx, method, contentType, body)
		if err == nil {
			return res, nil
		}

		delay, ok := t.retryDelay(ctx, err, attempt)
		if !ok {
			return nil, err
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, errors.Join(err, ctx.Err())
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

func (t *Telegram) sendMessage(ctx context.Context, msg TelegramMessage) (int, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return -1, fmt.Errorf("failed to marshal payload: %w", err)
	}

	res, err := t.callWithRetry(ctx, "sendMessage", "application/json", data)
	if err != nil {
		return -1, err
	}
	return res.Result.MessageID, nil
}

// sendDocument sends a as a document replying to message replyTo.
func (t *Telegram) sendDocument(ctx context.Context, a Attachment, replyTo int, silent bool) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	fields := map[string]string{
		"chat_id":          t.ChatID,
		"reply_parameters": fmt.Sprintf(`{"message_id":%d,"allow_sending_without_reply":true}`, replyTo),
	}
	if t.MessageThreadID != 0 {
		fields["message_thread_id"] = strconv.Itoa(t.MessageThreadID)
	}
	if silent {
		fields["disable_notification"] = "true"
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return err
		}
	}

	name := a.Name
	if name == "" {
		name = "attachment"
	}
	fw, err := w.CreateFormFile("document", name)
	if err != nil {
		return err
	}
	if _, err := fw.Write(a.Data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	_, err = t.callWithRetry(ctx, "sendDocument", w.FormDataContentType(), buf.Bytes())
	return err
}

func (t *Telegram) deleteMessage(ctx context.Context, messageID int) error {
	data, err := json.Marshal(map[string]any{
		"chat_id":    t.ChatID,
		"message_id": messageID,
	})
	if err != nil {
		return err
	}
	_, err = t.call(ctx, "deleteMessage", "application/json", data)
	return err
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// truncateUTF16 cuts s to at most limit UTF-16 code units, ending with "…" if cut.
func truncateUTF16(s string, limit int) string {
	if utf16Len(s) <= limit {
		return s
	}

	const ellipsis = '…'
	limit -= utf16.RuneLen(ellipsis)
	n := 0
	for i, r := range s {
		n += utf16.RuneLen(r)
		if n > limit {
			return s[:i] + string(ellipsis)
		}
	}
	return s
}
