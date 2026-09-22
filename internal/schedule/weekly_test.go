package schedule

import (
	"context"
	"strings"
	"testing"
	"time"
	"wfuseat/internal/storage"
)

func TestWeeklyUsesDifferentIntervalsAcrossWeekendAndEnds(t *testing.T) {
	now := time.Date(2030, 1, 4, 7, 0, 0, 0, Zone)
	plan := make([]storage.DayPlan, 7)
	plan[4] = storage.DayPlan{Enabled: true, Start: "09:00", End: "10:00"}
	plan[0] = storage.DayPlan{Enabled: true, Start: "14:00", End: "15:00"}
	j, err := NewAutomaticJob(6299, "room", []string{"001"}, "2030-01-04", 4, "weekly", plan, now)
	if err != nil {
		t.Fatal(err)
	}
	if !j.AwaitWindow || j.Day != "2030-01-04" || j.Start != "09:00" {
		t.Fatal(j)
	}
	plan[0].Start = "00:00" // Persistence must own a snapshot, not a shared slice.
	db := newDB(t)
	db.Add(j)
	var attempts []storage.Job
	run := func(_ context.Context, j storage.Job) Outcome {
		attempts = append(attempts, j)
		return Outcome{"succeeded", "ok"}
	}
	for _, day := range []string{"2030-01-04", "2030-01-07"} {
		jobs, _ := db.Jobs()
		at, _ := time.ParseInLocation("2006-01-02 15:04", day+" 08:00", Zone)
		if err := refreshWindowJobs(db, jobs, at, func(j storage.Job) (time.Time, error) {
			if j.Day != day {
				t.Fatal(j.Day)
			}
			return at, nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := Poll(context.Background(), db, "", at, run); err != nil {
			t.Fatal(err)
		}
	}
	jobs, _ := db.Jobs()
	if len(attempts) != 2 || attempts[1].Start != "14:00" || attempts[1].End != "15:00" || jobs[0].State != "completed" {
		t.Fatal(attempts, jobs)
	}
}
func TestAutomaticRejectsUnconfiguredDayAndEmptyDateRange(t *testing.T) {
	now := time.Date(2030, 1, 4, 7, 0, 0, 0, Zone)
	plan := make([]storage.DayPlan, 7)
	plan[0].Enabled = true
	if _, e := NewAutomaticJob(6299, "room", []string{"1"}, "2030-01-04", 7, "weekly", plan, now); e == nil || !strings.Contains(e.Error(), "周一") {
		t.Fatal(e)
	}
	plan[0].Start = "08:00"
	plan[0].End = "09:00"
	if _, e := NewAutomaticJob(6299, "room", []string{"1"}, "2030-01-04", 3, "weekly", plan, now); e == nil {
		t.Fatal("accepted date range without enabled weekdays")
	}
	for _, days := range []int{-1, 367} {
		if _, e := NewAutomaticJob(6299, "room", []string{"1"}, "2030-01-04", days, "weekly", plan, now); e == nil {
			t.Fatal("accepted invalid duration")
		}
	}
}

func TestIndefiniteAutomaticSurvivesMultipleWeeks(t *testing.T) {
	now := time.Date(2030, 1, 4, 7, 0, 0, 0, Zone)
	plan := make([]storage.DayPlan, 7)
	plan[4] = storage.DayPlan{Enabled: true, Start: "09:00", End: "10:00"}
	j, e := NewAutomaticJob(6299, "room", []string{"001"}, "2030-01-04", 0, "weekly", plan, now)
	if e != nil || !j.RepeatForever || j.RepeatUntil != "" {
		t.Fatal(j, e)
	}
	db := newDB(t)
	db.Add(j)
	for i := 0; i < 12; i++ {
		at := now.AddDate(0, 0, 7*i)
		jobs, _ := db.Jobs()
		refreshWindowJobs(db, jobs, at, func(storage.Job) (time.Time, error) { return at, nil })
		if e := Poll(context.Background(), db, "", at, func(context.Context, storage.Job) Outcome { return Outcome{"succeeded", "ok"} }); e != nil {
			t.Fatal(e)
		}
	}
	jobs, _ := db.Jobs()
	if jobs[0].State != "pending" || jobs[0].Day != now.AddDate(0, 0, 84).Format("2006-01-02") {
		t.Fatal(jobs)
	}
}
