// Package telegram delivers account-scoped daily summaries without exposing credentials.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Settings struct {
	ClearToken      bool   `json:"clear_token,omitempty"`
	Enabled         bool   `json:"enabled"`
	BotToken        string `json:"bot_token,omitempty"`
	TokenConfigured bool   `json:"token_configured,omitempty"`
	ChatID          string `json:"chat_id,omitempty"`
	Nickname        string `json:"nickname,omitempty"`
}

var tokenRE = regexp.MustCompile(`^[0-9]{5,20}:[A-Za-z0-9_-]{20,100}$`)
var chatRE = regexp.MustCompile(`^-?[0-9]{1,20}$`)

func (s Settings) Public() Settings {
	s.ClearToken = false
	s.TokenConfigured = s.BotToken != "" || s.TokenConfigured
	s.BotToken = ""
	return s
}
func (s Settings) Validate() error {
	if s.BotToken != "" && !tokenRE.MatchString(s.BotToken) {
		return errors.New("Telegram Bot Token 格式无效")
	}
	if s.ChatID != "" && !chatRE.MatchString(s.ChatID) {
		return errors.New("Telegram Chat ID 需要数字（群组可为负数）")
	}
	if len([]rune(s.Nickname)) > 32 {
		return errors.New("推送称呼最多 32 个字符")
	}
	if s.Enabled && (s.BotToken == "" && !s.TokenConfigured || s.ChatID == "") {
		return errors.New("请先填写 Telegram Bot Token 和 Chat ID")
	}
	return nil
}

// DeliveryError contains only safe text, never a URL (the URL contains the token).
// Only a definitive 429 response is retried; ambiguous sends are not duplicated.
type DeliveryError struct {
	Message    string
	RetryAfter time.Duration
}

func (e *DeliveryError) Error() string { return e.Message }

type Sender struct{ Client *http.Client }

func NewSender() *Sender {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return &Sender{Client: &http.Client{Transport: t, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (s *Sender) Send(ctx context.Context, cfg Settings, text string) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if cfg.BotToken == "" {
		return errors.New("Telegram Token 未配置")
	}
	return s.SendMessage(ctx, cfg, Message{Text: text})
}
func (s *Sender) SendMessage(ctx context.Context, cfg Settings, m Message) error {
	if e := cfg.Validate(); e != nil {
		return e
	}
	payload := map[string]any{"chat_id": cfg.ChatID, "text": m.Text, "parse_mode": "HTML", "link_preview_options": map[string]bool{"is_disabled": true}}
	if len(m.Keyboard) > 0 {
		payload["reply_markup"] = map[string]any{"inline_keyboard": m.Keyboard}
	}
	return s.Action(ctx, cfg, "sendMessage", payload)
}
func (s *Sender) Action(ctx context.Context, cfg Settings, method string, payload map[string]any) error {
	if method != "sendMessage" && method != "editMessageText" && method != "answerCallbackQuery" {
		return errors.New("不支持的 Telegram 操作")
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+cfg.BotToken+"/"+method, bytes.NewReader(b))
	if err != nil {
		return errors.New("Telegram 请求构造失败")
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := s.Client.Do(req)
	if err != nil {
		return &DeliveryError{Message: "Telegram 发送结果未知；请检查网络和聊天记录，未自动重发"}
	}
	defer res.Body.Close()
	var reply struct {
		OK          bool   `json:"ok"`
		Code        int    `json:"error_code"`
		Description string `json:"description"`
		Parameters  struct {
			Retry int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&reply); err != nil {
		return &DeliveryError{Message: "Telegram 响应无法确认，未自动重发"}
	}
	if res.StatusCode == 200 && reply.OK {
		return nil
	}
	if res.StatusCode == 429 && !reply.OK && reply.Code == 429 {
		seconds := reply.Parameters.Retry
		if seconds < 30 {
			seconds = 30
		}
		if seconds > 86400 {
			seconds = 86400
		}
		return &DeliveryError{Message: "Telegram 限流，稍后重试", RetryAfter: time.Duration(seconds) * time.Second}
	}
	if method == "editMessageText" && reply.Code == 400 && strings.HasPrefix(reply.Description, "Bad Request: message is not modified") {
		return nil
	}
	switch reply.Code {
	case 401:
		return &DeliveryError{Message: "Telegram Token 无效，请重新配置"}
	case 400, 403:
		return &DeliveryError{Message: "Telegram 无法投递，请检查 Chat ID、先私聊机器人 /start 及群组权限"}
	default:
		return &DeliveryError{Message: "Telegram 未确认送达，请检查聊天记录"}
	}
}

type Item struct {
	Room, Day, Start, End, State string
	Detail                       string
	Seats                        []string
}

func short(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
func esc(s string, n int) string { return html.EscapeString(short(s, n)) }
func Render(day string, cfg Settings, items []Item) string {
	name := cfg.Nickname
	if name == "" {
		name = "同学"
	}
	good, failed, unknown := 0, 0, 0
	for _, v := range items {
		switch v.State {
		case "succeeded":
			good++
		case "failed", "missed", "needs_login", "paused", "not_run":
			failed++
		default:
			unknown++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📚 <b>自习座位 · 每日回信</b>\n%s · 北京时间\n\n%s，今天的预约进展来啦。\n\n✅ 成功 %d  ·  🟠 未成功 %d  ·  ❔ 待核实 %d\n", esc(day, 10), esc(name, 32), good, failed, unknown)
	for i, v := range items {
		if i >= 8 {
			fmt.Fprintf(&b, "\n…其余 %d 项请在 TUI 预订记录中查看。\n", len(items)-i)
			break
		}
		label := "❔ 结果未知，请核实"
		switch v.State {
		case "succeeded":
			label = "✅ 预约成功"
		case "failed":
			label = "🟠 本次未成功"
		case "missed":
			label = "⏳ 错过执行时间"
		case "needs_login":
			label = "🔑 需要重新扫码"
		case "not_run":
			label = "⏳ 尚未执行，请检查调度与登录"
		case "paused":
			label = "⏸ 已暂停，未完成"
		}
		fmt.Fprintf(&b, "\n<b>%s</b>\n📍 %s\n🗓 %s  %s–%s\n🪑 候选顺序：%s\n", label, esc(v.Room, 48), esc(v.Day, 10), esc(v.Start, 5), esc(v.End, 5), esc(strings.Join(v.Seats, " → "), 40))
	}
	footer := "今天先到这里，明天继续为专注留一个位置。🌿"
	if good > 0 && failed == 0 && unknown == 0 {
		footer = "座位准备好了，带上书和好心情出发吧。☀️"
	}
	if unknown > 0 {
		footer = "有结果尚未确认，请先到学校预约记录核实，避免重复预约。🔎"
	}
	b.WriteString("\n" + footer + "\n<i>按执行日汇总；预约日期见各项。详情以学校记录为准。</i>")
	return b.String()
}

// Merge preserves an existing token when a remote edit leaves it blank.
func Merge(old, edit Settings) (Settings, error) {
	if edit.ClearToken {
		edit.BotToken = ""
	} else if edit.BotToken == "" {
		edit.BotToken = old.BotToken
	}
	edit.ClearToken = false
	edit.TokenConfigured = false
	return edit, edit.Validate()
}

// NewSenderWithProxy isolates optional Telegram routing from school traffic.
func NewSenderWithProxy(proxy string) (*Sender, error) {
	s := NewSender()
	if proxy == "" {
		return s, nil
	}
	u, e := url.Parse(proxy)
	if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("Telegram 代理需要有效的 HTTP(S) 地址")
	}
	s.Client.Transport.(*http.Transport).Proxy = http.ProxyURL(u)
	return s, nil
}
