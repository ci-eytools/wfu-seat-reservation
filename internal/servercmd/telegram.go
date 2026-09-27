package servercmd

import (
	"errors"
	"flag"
	"os"
	"strings"
	"time"
	"wfuseat/internal/notify"
	"wfuseat/internal/telegram"
)

type TelegramFlags struct {
	token, file, chat, name string
	proxy                   string
	delay, window           time.Duration
}

func RegisterTelegram(fs *flag.FlagSet) *TelegramFlags {
	v := &TelegramFlags{}
	// Environment secrets are resolved after parsing, never used as help defaults.
	fs.StringVar(&v.token, "telegram-bot-token", "", "统一推送 Bot Token；也可使用 WFUSEAT_TELEGRAM_BOT_TOKEN")
	fs.StringVar(&v.file, "telegram-token-file", "", "从私有文件读取 Bot Token，避免命令行暴露")
	fs.StringVar(&v.chat, "telegram-chat-id", "", "统一接收群或私聊 ID；也可使用 WFUSEAT_TELEGRAM_CHAT_ID")
	fs.StringVar(&v.proxy, "telegram-proxy", "", "仅 Telegram 使用的 HTTP(S) 代理；默认直连")
	fs.StringVar(&v.name, "telegram-name", "notice", "推送标题与称呼")
	fs.DurationVar(&v.delay, "telegram-delay", time.Minute, "预约批次开始后等待多久汇总")
	fs.DurationVar(&v.window, "telegram-batch-window", time.Minute, "同一批的计划时间窗口，例如 1m 或 5m")
	return v
}
func (v *TelegramFlags) Resolve() (notify.BatchConfig, error) {
	token := strings.TrimSpace(v.token)
	if v.file != "" {
		if token != "" {
			return notify.BatchConfig{}, errors.New("Telegram Token 参数与文件不能同时指定")
		}
		b, e := os.ReadFile(v.file)
		if e != nil {
			return notify.BatchConfig{}, errors.New("无法读取 Telegram Token 文件")
		}
		if len(b) > 4096 {
			return notify.BatchConfig{}, errors.New("Telegram Token 文件过大")
		}
		token = strings.TrimSpace(string(b))
	} else if token == "" {
		token = strings.TrimSpace(os.Getenv("WFUSEAT_TELEGRAM_BOT_TOKEN"))
	}
	chat := strings.TrimSpace(v.chat)
	if chat == "" {
		chat = strings.TrimSpace(os.Getenv("WFUSEAT_TELEGRAM_CHAT_ID"))
	}
	c := notify.BatchConfig{Proxy: strings.TrimSpace(v.proxy), Settings: telegram.Settings{Enabled: token != "" || chat != "", BotToken: token, ChatID: chat, Nickname: strings.TrimSpace(v.name)}, Delay: v.delay, Window: v.window}
	return c, c.Validate()
}
