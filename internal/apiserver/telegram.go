package apiserver

import (
	"context"
	"crypto/subtle"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"wfuseat/internal/notify"
	"wfuseat/internal/telegram"
)

type telegramAction func(context.Context, telegram.Settings, string, map[string]any) error
type callbackRequest struct {
	ID      string `json:"id"`
	Data    string `json:"data"`
	Message struct {
		ID   int64 `json:"message_id"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

func (s *Server) telegramCallback(w http.ResponseWriter, r *http.Request) {
	sender, e := telegram.NewSenderWithProxy(s.opts.Telegram.Proxy)
	if e != nil {
		problem(w, 503, "通知网络不可用")
		return
	}
	defer sender.Client.CloseIdleConnections()
	s.handleTelegramCallback(w, r, sender.Action)
}
func (s *Server) handleTelegramCallback(w http.ResponseWriter, r *http.Request, action telegramAction) {
	cfg := s.opts.Telegram.Settings
	if !cfg.Enabled || r.Method != "POST" {
		problem(w, 404, "接口不存在")
		return
	}
	want := notify.CallbackKey(cfg.BotToken)
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
		problem(w, 401, "未授权")
		return
	}
	var q callbackRequest
	if !decode(w, r, &q) {
		return
	}
	if q.ID == "" || len(q.ID) > 256 || q.Message.ID <= 0 || strconv.FormatInt(q.Message.Chat.ID, 10) != cfg.ChatID {
		problem(w, 400, "回调来源不匹配")
		return
	}
	message, e := notify.ExpandReport(s.auth, q.Data, cfg.ChatID, s.opts.Now())
	if e != nil {
		problem(w, 404, "详情不存在或已过期")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	// Dismiss Telegram's spinner promptly; never echo callback content in logs.
	_ = action(ctx, cfg, "answerCallbackQuery", map[string]any{"callback_query_id": q.ID})
	if e = action(ctx, cfg, "editMessageText", map[string]any{"chat_id": cfg.ChatID, "message_id": q.Message.ID, "text": message.Text, "parse_mode": "HTML", "reply_markup": map[string]any{"inline_keyboard": message.Keyboard}}); e != nil {
		problem(w, 502, "详情展开未确认，请稍后重试")
		return
	}
	actionName := "展开或翻页"
	if strings.HasSuffix(q.Data, ":s") {
		actionName = "收起"
	}
	log.Printf("Telegram 详情按钮处理成功：%s", actionName)
	reply(w, 200, map[string]bool{"ok": true})
}
