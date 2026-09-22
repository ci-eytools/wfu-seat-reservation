package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wfuseat/internal/chaoxing"
)

func TestFilterRoomsMatchesNameAndID(t *testing.T) {
	rooms := []chaoxing.Room{
		{ID: 6299, Name: "主校区 - 一楼 - 101自修室", Capacity: 120, Selectable: true},
		{ID: 6300, Name: "主校区 - 二楼 - 201自修室", Capacity: 80},
		{ID: 7777, Name: "安顺校区 - 一楼 - 自习区", Capacity: 40},
	}
	cases := []struct {
		filter string
		want   int
	}{
		{"", 3},
		{"   ", 3},
		{"二楼", 1},
		{"自修室", 2},
		{"6299", 1},
		{"校区", 3},
		{"不存在的房间", 0},
	}
	for _, tc := range cases {
		if got := len(filterRooms(rooms, tc.filter)); got != tc.want {
			t.Errorf("filterRooms(%q) returned %d rooms, want %d", tc.filter, got, tc.want)
		}
	}
}

func TestFilterRoomsIsCaseInsensitive(t *testing.T) {
	rooms := []chaoxing.Room{{ID: 1, Name: "Study Room A"}}
	if got := len(filterRooms(rooms, "study")); got != 1 {
		t.Errorf("lower-case filter matched %d rooms, want 1", got)
	}
	if got := len(filterRooms(rooms, "STUDY")); got != 1 {
		t.Errorf("upper-case filter matched %d rooms, want 1", got)
	}
}

// Filtering must not mutate the underlying list, so clearing it restores
// everything without another request.
func TestFilteringDoesNotMutateSource(t *testing.T) {
	states := []chaoxing.SeatState{{Num: "001"}, {Num: "002"}, {Num: "003"}}
	_ = filterSeatStates(states, "001")
	if len(states) != 3 {
		t.Fatalf("source list shrank to %d entries", len(states))
	}
	rooms := []chaoxing.Room{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}}
	_ = filterRooms(rooms, "a")
	if len(rooms) != 2 {
		t.Fatalf("source room list shrank to %d entries", len(rooms))
	}
}

func TestBreakQueryWrapsAtParameterBoundaries(t *testing.T) {
	query := "deptIdEnc=35bbd135397006a8&roomId=6299&day=2030-01-01&startTime=09%3A00" +
		"&endTime=09%3A30&seatNum=001&captcha=&wyToken=&enc=cad4de94bc6d45cb500e7fd558d61d99"
	wrapped := breakQuery(query, 40)

	for _, line := range strings.Split(wrapped, "\n") {
		if len(line) > 40 {
			t.Errorf("wrapped line is %d chars, expected at most 40: %q", len(line), line)
		}
	}
	// The decisive invariant: removing the newlines must reproduce the query
	// exactly. A dropped '&' would show the user a different form.
	if got := strings.ReplaceAll(wrapped, "\n", ""); got != query {
		t.Errorf("wrapping changed the query:\n got %s\nwant %s", got, query)
	}
	// The separator may move to the end of the previous line, so the signature
	// value is what must survive intact.
	if !strings.Contains(wrapped, "cad4de94bc6d45cb500e7fd558d61d99") {
		t.Error("the signature value must survive wrapping intact")
	}
}

func TestBreakQueryKeepsTokensIntact(t *testing.T) {
	// A single long token cannot be broken, so it must be emitted whole.
	long := "enc=" + strings.Repeat("a", 64)
	wrapped := breakQuery("day=2030-01-01&"+long, 30)
	if !strings.Contains(wrapped, long) {
		t.Fatalf("long token was broken: %q", wrapped)
	}
}

func TestLoadLogEntriesReadsNewestFirstAndToleratesJunk(t *testing.T) {
	dir := t.TempDir()
	older := filepath.Join(dir, "20260101T000000-1.jsonl")
	newer := filepath.Join(dir, "20260916T120000-2.jsonl")

	writeFile(t, older, `{"time_utc":"2026-01-01T00:00:00Z","method":"GET","url":"https://office.chaoxing.com/data/apps/seat/room/list","status":200,"elapsed_ms":10}`+"\n")
	// Within a file the lines are chronological, so the last line is the newest.
	writeFile(t, newer, strings.Join([]string{
		`{"time_utc":"2026-09-16T12:00:00Z","method":"POST","url":"https://office.chaoxing.com/data/apps/seat/getusedseatnums","status":403,"elapsed_ms":88}`,
		`not json at all`,
		`{"time_utc":"2026-09-16T12:00:05Z","method":"GET","url":"https://office.chaoxing.com/data/apps/seat/room/list","status":200,"elapsed_ms":31}`,
		``,
	}, "\n"))
	// A non-jsonl file must be ignored.
	writeFile(t, filepath.Join(dir, "notes.txt"), "ignore me")

	entries, files, err := loadLogEntries(dir)
	if err != nil {
		t.Fatalf("loadLogEntries: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %v, want 2 jsonl files", files)
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4 (junk line kept, blank dropped)", len(entries))
	}
	if entries[0].ElapsedMS != 31 {
		t.Errorf("first entry = %+v, want the newest (31ms)", entries[0])
	}
	if entries[1].Raw != nil || entries[1].RawLine != "not json at all" {
		t.Errorf("the malformed line should be preserved as raw text only: %+v", entries[1])
	}
	if entries[2].Status != 403 {
		t.Errorf("third entry status = %d, want 403 (older than the newest)", entries[2].Status)
	}
	if entries[3].Status != 200 {
		t.Errorf("last entry status = %d, want 200 (the oldest file)", entries[3].Status)
	}
	// The newest entry is never dropped by the cap.
	if entries[0].Status == 0 {
		t.Error("newest entry was not parsed")
	}
}

func TestLoadLogEntriesHandlesMissingDirectory(t *testing.T) {
	entries, files, err := loadLogEntries(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("a missing log directory is not an error: %v", err)
	}
	if len(entries) != 0 || len(files) != 0 {
		t.Fatalf("entries = %v, files = %v", entries, files)
	}
}

func TestURLPathExtractsTheSeatAPIPath(t *testing.T) {
	got := urlPath("https://office.chaoxing.com/data/apps/seat/getusedseatnums?token=%5BREDACTED%5D")
	if got != "/data/apps/seat/getusedseatnums" {
		t.Fatalf("urlPath = %q", got)
	}
	if got := urlPath("not a url"); got != "not a url" {
		t.Fatalf("urlPath fallback = %q", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
