package telegram

import (
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestBatchPaginationEscapingAndPersonalization(t *testing.T) {
	rows := make([]BatchItem, 17)
	for i := range rows {
		rows[i] = BatchItem{Account: "张<&>" + strings.Repeat("🪑", 40), Item: Item{State: "succeeded", Room: strings.Repeat("<&🪑", 50), Day: "2026-09-28", Start: "08:00", End: "10:00", Seats: []string{"001", "002"}}}
	}
	pages := RenderBatch(time.Date(2026, 9, 27, 22, 15, 0, 0, time.FixedZone("CST", 8*3600)), sample(), rows)
	if len(pages) != 3 {
		t.Fatal("dropped rows")
	}
	for _, s := range pages {
		if len(utf16.Encode([]rune(s))) > 4096 {
			t.Fatal("message exceeds limit")
		}
		if strings.Contains(s, "张<&>") || !strings.Contains(s, "张&lt;&amp;&gt;") {
			t.Fatal("unsafe HTML")
		}
		if !strings.Contains(s, "座位就绪") || !strings.Contains(s, "22:15") {
			t.Fatal("missing presentation")
		}
	}
}
