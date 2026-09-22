package schedule

import (
	"context"
	"errors"
	"testing"
	"time"
	"wfuseat/internal/storage"
)

func TestStartupRefreshReplacesOldGlobalClockPerRoom(t *testing.T) {
	db, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	now := time.Now().Truncate(time.Millisecond)
	jobs := []storage.Job{{ID: "a", RoomID: 1, Day: "2030-01-02", State: "pending", Next: now, GlobalTime: "08:00:00"}, {ID: "b", RoomID: 2, Day: "2030-01-02", State: "pending", Next: now, GlobalTime: "08:00:00"}}
	for _, j := range jobs {
		db.Add(j)
	}
	at := now.Add(time.Hour)
	e = refreshWindowJobs(db, jobs, now, func(j storage.Job) (time.Time, error) { return at.Add(time.Duration(j.RoomID) * time.Hour), nil })
	if e != nil {
		t.Fatal(e)
	}
	saved, _ := db.Jobs()
	for _, j := range saved {
		if !j.RoomWindow || !j.Next.Equal(at.Add(time.Duration(j.RoomID)*time.Hour)) {
			t.Fatal(j)
		}
	}
	calls := 0
	e = Poll(context.Background(), db, "08:00:00", now, func(context.Context, storage.Job) Outcome { calls++; return Outcome{} })
	if e != nil || calls != 0 {
		t.Fatal(e, calls)
	}
	after, _ := db.Jobs()
	for i, j := range after {
		if !j.Next.Equal(saved[i].Next) {
			t.Fatal("global time overrode server", j)
		}
	}
}
func TestWindowLookupFailureDoesNotInventTime(t *testing.T) {
	db, _ := storage.Open(t.TempDir())
	defer db.Close()
	j := storage.Job{ID: "a", State: "pending", Next: time.Now().Add(time.Hour)}
	db.Add(j)
	e := refreshWindowJobs(db, []storage.Job{j}, time.Now(), func(storage.Job) (time.Time, error) { return time.Time{}, errors.New("unavailable") })
	if e == nil {
		t.Fatal("failure swallowed")
	}
	jobs, _ := db.Jobs()
	if jobs[0].RoomWindow {
		t.Fatal("guessed window")
	}
}
