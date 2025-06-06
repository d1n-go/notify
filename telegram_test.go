package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

func makeTelegramWithServer(t *testing.T, deleteOK, sendOK bool, sendMsgID int) (*Telegram, func()) {
	mux := http.NewServeMux()
	mux.HandleFunc("/bot123/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		if sendOK {
			w.WriteHeader(http.StatusOK)
			resp := TelegramResponse{
				Ok: true,
				Result: struct {
					MessageID int `json:"message_id"`
				}{MessageID: sendMsgID},
			}
			json.NewEncoder(w).Encode(resp)
		} else {
			http.Error(w, "bad request", http.StatusBadRequest)
		}
	})
	mux.HandleFunc("/bot123/deleteMessage", func(w http.ResponseWriter, r *http.Request) {
		if deleteOK {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		} else {
			http.Error(w, "delete failed", http.StatusBadRequest)
		}
	})

	ts := httptest.NewServer(mux)

	// Подменяем apiURL глобально
	orig := apiURL
	apiURL = ts.URL + "/bot"

	teleg := &Telegram{
		TelegramParams: TelegramParams{
			Token:              "123",
			ChatID:             "chat42",
			DeletePrevMessages: true,
			MS:                 &mockStorage{lastID: 101},
		},
	}

	return teleg, func() {
		ts.Close()
		apiURL = orig
	}
}

func TestTelegramSendSuccess(t *testing.T) {
	tg, cleanup := makeTelegramWithServer(t, true, true, 555)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := tg.Send(ctx, "Subject", "Body text")
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	if tg.MS.(*mockStorage).lastID != 555 {
		t.Errorf("expected stored message ID 555, got %d", tg.MS.(*mockStorage).lastID)
	}
}

func TestTelegramSendSendFails(t *testing.T) {
	tg, cleanup := makeTelegramWithServer(t, true, false, 0)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := tg.Send(ctx, "FailSubject", "FailBody")
	if err == nil {
		t.Fatal("expected error due to send failure, got nil")
	}
}

func TestTelegramSendDeleteFailsButSendOK(t *testing.T) {
	tg, cleanup := makeTelegramWithServer(t, false, true, 888)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := tg.Send(ctx, "IgnoreDel", "StillSend")
	if err != nil {
		t.Fatalf("expected send to succeed despite delete failure, got: %v", err)
	}

	if tg.MS.(*mockStorage).lastID != 888 {
		t.Errorf("expected stored message ID 888, got %d", tg.MS.(*mockStorage).lastID)
	}
}
