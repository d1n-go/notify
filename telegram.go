package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

var apiURL = "https://api.telegram.org/bot"

type TelegramParams struct {
	Token              string
	ChatID             string
	DeletePrevMessages bool
	MS                 MessageStorage
}

type MessageStorage interface {
	StoreMessageID(messageID int) error
	GetMessageID() (int, error)
}

type Telegram struct {
	TelegramParams
}

func NewTelegram(telegramParams TelegramParams) *Telegram {
	return &Telegram{
		TelegramParams: telegramParams,
	}
}

type TelegramMessage struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

type TelegramResponse struct {
	Ok     bool `json:"ok"`
	Result struct {
		MessageID int `json:"message_id"`
	} `json:"result"`
}

func (t *Telegram) Send(ctx context.Context, subject string, msg string) error {
	msgID := -1
	var err error

	if t.DeletePrevMessages && t.MS != nil {
		msgID, err = t.MS.GetMessageID()
		if err == nil {
			_ = t.deleteMessage(ctx, msgID)
		}
	}
	msgID, err = t.sendMessage(ctx, fmt.Sprintf("[%s]\n\n%s", subject, msg))
	if t.DeletePrevMessages && err == nil && msgID != -1 && t.MS != nil {
		_ = t.MS.StoreMessageID(msgID)
	}

	return err
}

func (t *Telegram) postJSON(ctx context.Context, method string, payload any) (*http.Response, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	url := fmt.Sprintf("%s%s/%s", apiURL, t.Token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	return resp, nil
}

func (t *Telegram) sendMessage(ctx context.Context, msg string) (int, error) {
	payload := TelegramMessage{
		ChatID: t.ChatID,
		Text:   msg,
	}

	resp, err := t.postJSON(ctx, "sendMessage", payload)
	if err != nil {
		return -1, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return -1, fmt.Errorf("unexpected response: %s", resp.Status)
	}

	var res TelegramResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return -1, fmt.Errorf("failed to decode response: %w", err)
	}
	if !res.Ok {
		return -1, fmt.Errorf("telegram API error")
	}
	return res.Result.MessageID, nil
}

func (t *Telegram) deleteMessage(ctx context.Context, messageID int) error {
	payload := map[string]interface{}{
		"chat_id":    t.ChatID,
		"message_id": messageID,
	}

	resp, err := t.postJSON(ctx, "deleteMessage", payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected response: %s", resp.Status)
	}

	var res TelegramResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if !res.Ok {
		return fmt.Errorf("telegram API error")
	}

	return nil
}
