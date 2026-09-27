package apiserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"wfuseat/internal/notify"
	"wfuseat/internal/telegram"
)

func TestCallbackAuthAndExpansion(t *testing.T) {
	cfg := notify.BatchConfig{Settings: telegram.Settings{Enabled: true, BotToken: "123456:" + strings.Repeat("x", 35), ChatID: "-123"}, Window: time.Minute, Delay: time.Minute}
	s, e := New(Options{Root: t.TempDir(), Telegram: cfg})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	message, e := notify.SaveReport(s.auth, "-123", "成功/全部：1/2", []string{"详细内容"}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	q := callbackRequest{ID: "callback-id", Data: message.Keyboard[0][0].Data}
	q.Message.ID = 74
	q.Message.Chat.ID = -123
	raw, _ := json.Marshal(q)
	calls := 0
	action := func(_ context.Context, _ telegram.Settings, method string, payload map[string]any) error {
		calls++
		if method == "editMessageText" && payload["text"] != "详细内容" {
			t.Fatal("wrong details")
		}
		return nil
	}
	for _, valid := range []bool{false, true} {
		r := httptest.NewRequest("POST", "/v1/telegram/callback", strings.NewReader(string(raw)))
		if valid {
			r.Header.Set("Authorization", "Bearer "+notify.CallbackKey(cfg.Settings.BotToken))
		}
		w := httptest.NewRecorder()
		s.handleTelegramCallback(w, r, action)
		expected := 401
		if valid {
			expected = 200
		}
		if w.Code != expected {
			t.Fatalf("status %d", w.Code)
		}
	}
	if calls != 2 {
		t.Fatal("unauthorized call or missing expansion")
	}
}
