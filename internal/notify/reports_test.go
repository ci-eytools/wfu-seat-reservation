package notify

import (
	"strings"
	"testing"
	"time"
)

func TestReportButtonsExpandCollapseAndScope(t *testing.T) {
	db := dbFor(t)
	now := time.Now()
	m, e := SaveReport(db, "-123", "summary", []string{"page one", "page two"}, now)
	if e != nil {
		t.Fatal(e)
	}
	data := m.Keyboard[0][0].Data
	if len(data) > 64 {
		t.Fatal("Telegram callback_data limit")
	}
	first, e := ExpandReport(db, data, "-123", now)
	if e != nil || first.Text != "page one" {
		t.Fatal(e)
	}
	next := first.Keyboard[0][1].Data
	second, e := ExpandReport(db, next, "-123", now)
	if e != nil || second.Text != "page two" {
		t.Fatal(e)
	}
	collapse := second.Keyboard[0][1].Data
	small, e := ExpandReport(db, collapse, "-123", now)
	if e != nil || small.Text != "summary" {
		t.Fatal(e)
	}
	if _, e = ExpandReport(db, data, "-999", now); e == nil {
		t.Fatal("cross-chat access")
	}
	if _, e = ExpandReport(db, data, "-123", now.Add(8*24*time.Hour)); e == nil {
		t.Fatal("expired")
	}
	if strings.Contains(CallbackKey("secret"), "secret") {
		t.Fatal("token exposed")
	}
}
