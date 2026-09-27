package notify

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"wfuseat/internal/storage"
	"wfuseat/internal/telegram"
)

type Report struct {
	ChatID  string
	Created time.Time
	Summary string
	Pages   []string
}

func CallbackKey(token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("wfuseat-callback-v1"))
	return hex.EncodeToString(mac.Sum(nil))
}
func SaveReport(db *storage.Store, chat, summary string, pages []string, now time.Time) (telegram.Message, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return telegram.Message{}, e
	}
	id := hex.EncodeToString(b)
	raw, e := json.Marshal(Report{ChatID: chat, Created: now, Summary: summary, Pages: pages})
	if e != nil {
		return telegram.Message{}, e
	}
	if e = db.Put("tg_report/"+id, raw); e != nil {
		return telegram.Message{}, e
	}
	return telegram.Message{Text: summary, Keyboard: [][]telegram.Button{{{Text: "全部详情", Data: "wfuseat:" + id + ":0"}}}}, nil
}
func ExpandReport(db *storage.Store, data, chat string, now time.Time) (telegram.Message, error) {
	bad := errors.New("详情不存在或已过期")
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != "wfuseat" || len(parts[1]) != 32 {
		return telegram.Message{}, bad
	}
	if _, e := hex.DecodeString(parts[1]); e != nil {
		return telegram.Message{}, bad
	}
	raw, e := db.Get("tg_report/" + parts[1])
	if e != nil {
		return telegram.Message{}, e
	}
	var r Report
	if json.Unmarshal(raw, &r) != nil || r.ChatID != chat || now.Sub(r.Created) > 7*24*time.Hour {
		return telegram.Message{}, bad
	}
	prefix := "wfuseat:" + parts[1] + ":"
	if parts[2] == "s" {
		return telegram.Message{Text: r.Summary, Keyboard: [][]telegram.Button{{{Text: "全部详情", Data: prefix + "0"}}}}, nil
	}
	page, e := strconv.Atoi(parts[2])
	if e != nil || page < 0 || page >= len(r.Pages) {
		return telegram.Message{}, bad
	}
	nav := []telegram.Button{}
	if page > 0 {
		nav = append(nav, telegram.Button{Text: "上一页", Data: prefix + strconv.Itoa(page-1)})
	}
	nav = append(nav, telegram.Button{Text: "收起详情", Data: prefix + "s"})
	if page+1 < len(r.Pages) {
		nav = append(nav, telegram.Button{Text: "下一页", Data: prefix + strconv.Itoa(page+1)})
	}
	return telegram.Message{Text: r.Pages[page], Keyboard: [][]telegram.Button{nav}}, nil
}
