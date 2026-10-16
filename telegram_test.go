package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

type mockStorage struct {
	lastID int
	getErr error
	setErr error
}

func (m *mockStorage) GetMessageID() (int, error) {
	return m.lastID, m.getErr
}

func (m *mockStorage) StoreMessageID(id int) error {
	m.lastID = id
	return m.setErr
}

type fakeTelegram struct {
	deleteOK  bool
	sendOK    bool
	sendMsgID int

	sentText  string
	deletedID int
}

func (f *fakeTelegram) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/bot123/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		var m TelegramMessage
		_ = json.NewDecoder(r.Body).Decode(&m)
		f.sentText = m.Text

		if !f.sendOK {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: chat not found"})
			return
		}
		var resp TelegramResponse
		resp.Ok = true
		resp.Result.MessageID = f.sendMsgID
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/bot123/deleteMessage", func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			MessageID int `json:"message_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		f.deletedID = m.MessageID

		if !f.deleteOK {
			http.Error(w, "delete failed", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	return mux
}

func makeTelegramWithServer(t *testing.T, f *fakeTelegram, storedID int) *Telegram {
	t.Helper()

	ts := httptest.NewServer(f.handler())
	t.Cleanup(ts.Close)

	tg := NewTelegram(TelegramParams{
		Token:              "123",
		ChatID:             "chat42",
		DeletePrevMessages: true,
		MS:                 &mockStorage{lastID: storedID},
	})
	tg.baseURL = ts.URL + "/bot"
	return tg
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestTelegramSendSuccess(t *testing.T) {
	f := &fakeTelegram{deleteOK: true, sendOK: true, sendMsgID: 555}
	tg := makeTelegramWithServer(t, f, 101)

	if err := tg.Send(testCtx(t), "Subject", "Body text"); err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	if f.deletedID != 101 {
		t.Errorf("expected previous message 101 to be deleted, got %d", f.deletedID)
	}
	if f.sentText != "[Subject]\n\nBody text" {
		t.Errorf("unexpected text: %q", f.sentText)
	}
	if got := tg.MS.(*mockStorage).lastID; got != 555 {
		t.Errorf("expected stored message ID 555, got %d", got)
	}
}

func TestTelegramSendSendFails(t *testing.T) {
	f := &fakeTelegram{deleteOK: true, sendOK: false}
	tg := makeTelegramWithServer(t, f, 101)

	err := tg.Send(testCtx(t), "FailSubject", "FailBody")
	if err == nil {
		t.Fatal("expected error due to send failure, got nil")
	}
	if !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("expected API description in error, got: %v", err)
	}
	if got := tg.MS.(*mockStorage).lastID; got != 101 {
		t.Errorf("stored message ID must not change on failure, got %d", got)
	}
}

func TestTelegramSendDeleteFailsButSendOK(t *testing.T) {
	f := &fakeTelegram{deleteOK: false, sendOK: true, sendMsgID: 888}
	tg := makeTelegramWithServer(t, f, 101)

	if err := tg.Send(testCtx(t), "IgnoreDel", "StillSend"); err != nil {
		t.Fatalf("expected send to succeed despite delete failure, got: %v", err)
	}

	if got := tg.MS.(*mockStorage).lastID; got != 888 {
		t.Errorf("expected stored message ID 888, got %d", got)
	}
}

func TestTelegramSendNoStoredMessage(t *testing.T) {
	f := &fakeTelegram{deleteOK: true, sendOK: true, sendMsgID: 1}
	tg := makeTelegramWithServer(t, f, 0)

	if err := tg.Send(testCtx(t), "First", "Message"); err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if f.deletedID != 0 {
		t.Errorf("expected no delete request, got delete of %d", f.deletedID)
	}
}

func TestTelegramSendTruncatesLongMessage(t *testing.T) {
	f := &fakeTelegram{sendOK: true, sendMsgID: 1}
	tg := makeTelegramWithServer(t, f, 0)

	if err := tg.Send(testCtx(t), "Long", strings.Repeat("я", 10000)); err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if n := len(utf16.Encode([]rune(f.sentText))); n != telegramMaxMessageLen {
		t.Errorf("expected text of %d UTF-16 units, got %d", telegramMaxMessageLen, n)
	}
	if !strings.HasSuffix(f.sentText, "…") {
		t.Errorf("expected truncated text to end with ellipsis")
	}
}

func TestTelegramErrorDoesNotLeakToken(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	ts.Close() // запросы будут падать с сетевой ошибкой

	tg := NewTelegram(TelegramParams{Token: "secret-token", ChatID: "chat42"})
	tg.baseURL = ts.URL + "/bot"

	err := tg.Send(testCtx(t), "Subject", "Body")
	if err == nil {
		t.Fatal("expected network error, got nil")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Errorf("error leaks bot token: %v", err)
	}
}

func TestTruncateUTF16(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{"short", "abc", 5, "abc"},
		{"exact", "abcde", 5, "abcde"},
		{"cut", "abcdef", 5, "abcd…"},
		// 😀 занимает 2 UTF-16 единицы и не должен разрезаться пополам
		{"surrogate pair", "ab😀cd", 4, "ab…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateUTF16(tt.in, tt.limit); got != tt.want {
				t.Errorf("truncateUTF16(%q, %d) = %q, want %q", tt.in, tt.limit, got, tt.want)
			}
		})
	}
}
