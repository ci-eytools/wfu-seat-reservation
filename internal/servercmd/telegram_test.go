package servercmd

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTelegramFlagsEnvironmentSecretNotInHelp(t *testing.T) {
	secret := "123456:" + strings.Repeat("a", 35)
	t.Setenv("WFUSEAT_TELEGRAM_BOT_TOKEN", secret)
	t.Setenv("WFUSEAT_TELEGRAM_CHAT_ID", "-123")
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	f := RegisterTelegram(fs)
	var out bytes.Buffer
	fs.SetOutput(&out)
	fs.PrintDefaults()
	if strings.Contains(out.String(), secret) {
		t.Fatal("secret in help")
	}
	if e := fs.Parse(nil); e != nil {
		t.Fatal(e)
	}
	c, e := f.Resolve()
	if e != nil || c.Delay != time.Minute || c.Window != time.Minute || !c.Settings.Enabled {
		t.Fatal("defaults", e)
	}
}
func TestTelegramTokenFile(t *testing.T) {
	t.Setenv("WFUSEAT_TELEGRAM_BOT_TOKEN", "")
	t.Setenv("WFUSEAT_TELEGRAM_CHAT_ID", "")
	path := filepath.Join(t.TempDir(), "token")
	token := "123456:" + strings.Repeat("b", 35)
	if e := os.WriteFile(path, []byte(token+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	f := RegisterTelegram(fs)
	if e := fs.Parse([]string{"--telegram-token-file", path, "--telegram-chat-id", "-123", "--telegram-name", "晚安自习室"}); e != nil {
		t.Fatal(e)
	}
	c, e := f.Resolve()
	if e != nil || c.Settings.BotToken != token {
		t.Fatal(e)
	}
}
