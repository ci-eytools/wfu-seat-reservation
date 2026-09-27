package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func sample() Settings {
	return Settings{Enabled: true, BotToken: "123456:" + strings.Repeat("a", 35), ChatID: "-123", Nickname: "小林<&>"}
}
func TestSendAndSafeErrors(t *testing.T) {
	cfg := sample()
	for _, tc := range []struct {
		body   string
		status int
		retry  bool
		ok     bool
	}{
		{`{"ok":true}`, 200, false, true},
		{`{"ok":false,"error_code":429,"parameters":{"retry_after":40}}`, 429, true, false},
		{`{"ok":false,"error_code":401,"description":"secret"}`, 401, false, false},
		{"not JSON", 502, false, false},
	} {
		sender := Sender{Client: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "api.telegram.org" || r.URL.Scheme != "https" || r.Method != "POST" {
				t.Fatal("invalid destination")
			}
			var body map[string]any
			if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
				t.Fatal(e)
			}
			if _, ok := body["reply_markup"]; ok {
				t.Fatal("empty inline keyboard should be omitted")
			}
			if body["chat_id"] != cfg.ChatID || body["parse_mode"] != "HTML" {
				t.Fatal("payload")
			}
			return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
		})}}
		err := sender.Send(context.Background(), cfg, "test")
		if (err == nil) != tc.ok {
			t.Fatalf("unexpected result %v", err)
		}
		if err != nil {
			var de *DeliveryError
			if !errors.As(err, &de) {
				t.Fatal(err)
			}
			if (de.RetryAfter > 0) != tc.retry {
				t.Fatal("retry policy")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), cfg.BotToken) {
				t.Fatal("secret exposed")
			}
		}
	}
	sender := Sender{Client: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) { return nil, errors.New(r.URL.String()) })}}
	if e := sender.Send(context.Background(), cfg, "test"); e == nil || strings.Contains(e.Error(), cfg.BotToken) {
		t.Fatal("network URL leaked")
	}
}
func TestRenderEscapesAndBounds(t *testing.T) {
	items := make([]Item, 100)
	for i := range items {
		items[i] = Item{Room: strings.Repeat("<&🪑", 100), Day: "2026-09-27", Start: "08:00", End: "09:00", Seats: []string{"001", "002"}, State: "unknown"}
	}
	text := Render("2026-09-26", sample(), items)
	if strings.Contains(text, "小林<&>") || !strings.Contains(text, "小林&lt;&amp;&gt;") || !strings.Contains(text, "待核实 100") {
		t.Fatal("escaping or state wrong")
	}
	// Telegram counts parsed text, but keep even the encoded message comfortably bounded.
	if len(utf16.Encode([]rune(text))) > 4096 {
		t.Fatalf("message too long: %d", len(utf16.Encode([]rune(text))))
	}
	if !strings.Contains(text, "其余 92 项") {
		t.Fatal("truncation missing")
	}
}
func TestMergeToken(t *testing.T) {
	cfg := sample()
	pub := cfg.Public()
	if pub.BotToken != "" || !pub.TokenConfigured {
		t.Fatal("public token")
	}
	merged, e := Merge(cfg, pub)
	if e != nil || merged.BotToken != cfg.BotToken {
		t.Fatal("blank did not preserve")
	}
	_, e = Merge(Settings{}, pub)
	if e == nil {
		t.Fatal("client forged configured flag")
	}
	pub.Enabled = false
	pub.ClearToken = true
	merged, e = Merge(cfg, pub)
	if e != nil || merged.BotToken != "" || merged.TokenConfigured {
		t.Fatal("clear failed")
	}
	if NewSender().Client.Timeout != 20*time.Second {
		t.Fatal("unbounded timeout")
	}
}

func TestDedicatedTelegramProxy(t *testing.T) {
	s, e := NewSenderWithProxy("http://127.0.0.1:17890")
	if e != nil {
		t.Fatal(e)
	}
	req, _ := http.NewRequest("GET", "https://api.telegram.org", nil)
	u, e := s.Client.Transport.(*http.Transport).Proxy(req)
	if e != nil || u.Host != "127.0.0.1:17890" {
		t.Fatal("proxy routing")
	}
	if NewSender().Client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("default inherits proxy")
	}
	if _, e = NewSenderWithProxy("file:///tmp/example"); e == nil {
		t.Fatal("invalid proxy accepted")
	}
}
