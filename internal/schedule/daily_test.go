package schedule

import (
	"context"
	"testing"
	"time"
	"wfuseat/internal/storage"
)

func dailyJob(t *testing.T, day string, days int, weekends bool) storage.Job {
	t.Helper()
	at, err := time.ParseInLocation("2006-01-02 15:04", day+" 08:00", Zone)
	if err != nil {
		t.Fatal(err)
	}
	j := storage.Job{ID: "daily", Day: day, Start: "09:00", End: "10:00", Next: at, RoomWindow: true, State: "pending", Seats: []string{"001"}}
	if err = ConfigureDaily(&j, days, weekends); err != nil {
		t.Fatal(err)
	}
	return j
}
func TestDailyCycleUsesFreshWindowAndStopsAtInclusiveEnd(t *testing.T) {
	db := newDB(t)
	// Friday through Monday: the weekend is skipped, and Monday is included.
	j := dailyJob(t, "2030-01-04", 4, true)
	if err := db.Add(j); err != nil {
		t.Fatal(err)
	}
	calls := 0
	execute := func(_ context.Context, j storage.Job) Outcome {
		calls++
		return Outcome{"succeeded", j.Day + " success"}
	}
	if err := Poll(context.Background(), db, "", j.Next, execute); err != nil {
		t.Fatal(err)
	}
	jobs, _ := db.Jobs()
	next := jobs[0]
	if next.Day != "2030-01-07" || !next.AwaitWindow || next.RoomWindow || next.State != "pending" {
		t.Fatalf("%+v", next)
	}
	// A provisional timestamp must never be dispatched, even after restart.
	if err := Poll(context.Background(), db, "", next.Next.Add(time.Hour), execute); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("dispatched without a fetched window")
	}
	opening := next.Next.Add(8*time.Hour + 17*time.Minute)
	if err := refreshWindowJobs(db, jobs, next.Next, func(j storage.Job) (time.Time, error) {
		if j.Day != "2030-01-07" {
			t.Fatal(j.Day)
		}
		return opening, nil
	}); err != nil {
		t.Fatal(err)
	}
	jobs, _ = db.Jobs()
	if jobs[0].AwaitWindow || !jobs[0].Next.Equal(opening) {
		t.Fatalf("%+v", jobs[0])
	}
	if err := Poll(context.Background(), db, "", opening, execute); err != nil {
		t.Fatal(err)
	}
	if err := Poll(context.Background(), db, "", opening.Add(24*time.Hour), execute); err != nil {
		t.Fatal(err)
	}
	jobs, _ = db.Jobs()
	history, _ := db.History(j.ID)
	if calls != 2 || jobs[0].State != "completed" || len(history) != 2 {
		t.Fatalf("calls=%d job=%+v history=%v", calls, jobs[0], history)
	}
}
func TestDailyFailureAdvancesButUnknownAndLoginPause(t *testing.T) {
	for _, state := range []string{"failed", "unknown", "needs_login", "paused"} {
		t.Run(state, func(t *testing.T) {
			db := newDB(t)
			j := dailyJob(t, "2030-01-04", 2, false)
			db.Add(j)
			Poll(context.Background(), db, "", j.Next, func(context.Context, storage.Job) Outcome { return Outcome{state, "test"} })
			jobs, _ := db.Jobs()
			got := jobs[0]
			if state == "failed" {
				if got.Day != "2030-01-05" || !got.AwaitWindow {
					t.Fatal(got)
				}
			} else if got.State != state || got.Day != j.Day || got.AwaitWindow {
				t.Fatal(got)
			}
		})
	}
}
func TestDailyWeekendStartAndCalendarRangeValidation(t *testing.T) {
	j := dailyJob(t, "2030-01-05", 3, true)
	if j.Day != "2030-01-07" || j.RepeatStart != "2030-01-05" || j.RepeatUntil != "2030-01-07" || !j.AwaitWindow {
		t.Fatal(j)
	}
	for _, days := range []int{0, 367, 1, 2} {
		j := storage.Job{Day: "2030-01-05"}
		if ConfigureDaily(&j, days, true) == nil {
			t.Fatalf("accepted days=%d", days)
		}
	}
}
func TestDailyFarFutureWaitsAndMissedDatesAreSkipped(t *testing.T) {
	db := newDB(t)
	j := dailyJob(t, "2030-01-04", 20, false)
	advanceDaily(&j, time.Date(2030, 1, 10, 11, 0, 0, 0, Zone))
	if j.Day != "2030-01-11" {
		t.Fatal(j)
	}
	db.Add(j)
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, Zone)
	calls := 0
	if err := refreshWindowJobs(db, []storage.Job{j}, now, func(storage.Job) (time.Time, error) { calls++; return now, nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || awaitingDailyWindow(db, now) {
		t.Fatal("far future window queried")
	}
	if !awaitingDailyWindow(db, now.AddDate(0, 0, 4)) {
		t.Fatal("eligible window not refreshed")
	}
}

func TestStaleWindowRefreshCannotRestorePreviousDailyOccurrence(t *testing.T) {
	db := newDB(t)
	j := dailyJob(t, "2030-01-04", 3, false)
	db.Add(j)
	err := refreshWindowJobs(db, []storage.Job{j}, j.Next, func(storage.Job) (time.Time, error) {
		if err := Poll(context.Background(), db, "", j.Next, func(context.Context, storage.Job) Outcome { return Outcome{"succeeded", "ok"} }); err != nil {
			t.Fatal(err)
		}
		return j.Next.Add(time.Second), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	jobs, _ := db.Jobs()
	if jobs[0].Day != "2030-01-05" || !jobs[0].AwaitWindow {
		t.Fatalf("stale response restored old day: %+v", jobs[0])
	}
}

func TestRestartPastUnresolvedDailyWindowSkipsExpiredDates(t *testing.T) {
	db := newDB(t)
	j := dailyJob(t, "2030-01-04", 5, false)
	j.AwaitWindow = true
	j.RoomWindow = false
	db.Add(j)
	now := time.Date(2030, 1, 6, 11, 0, 0, 0, Zone)
	noLookup := func(storage.Job) (time.Time, error) { t.Fatal("queried expired date"); return time.Time{}, nil }
	if err := refreshWindowJobs(db, []storage.Job{j}, now, noLookup); err != nil {
		t.Fatal(err)
	}
	jobs, _ := db.Jobs()
	if jobs[0].Day != "2030-01-07" || !jobs[0].AwaitWindow {
		t.Fatal(jobs)
	}
	if err := refreshWindowJobs(db, jobs, now.AddDate(0, 0, 5), noLookup); err != nil {
		t.Fatal(err)
	}
	jobs, _ = db.Jobs()
	if jobs[0].State != "completed" {
		t.Fatal(jobs)
	}
}
