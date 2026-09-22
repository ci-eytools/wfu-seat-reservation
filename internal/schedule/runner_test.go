package schedule

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"wfuseat/internal/chaoxing"
	"wfuseat/internal/config"
	"wfuseat/internal/storage"
)

func TestOrderedFallback(t *testing.T) {
	for _, tc := range []struct {
		name, mode, want string
		count            int
	}{{"success second", "refuse", "succeeded", 2}, {"timeout stops", "timeout", "unknown", 1}, {"unknown stops", "unknown", "unknown", 1}, {"all refused", "all", "failed", 3}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			o := TrySeats(context.Background(), []string{"003", "001", "002"}, func(_ context.Context, s string) (*chaoxing.ReserveResult, error) {
				calls = append(calls, s)
				if tc.mode == "timeout" {
					return nil, errors.New("timeout")
				}
				if tc.mode == "unknown" {
					return &chaoxing.ReserveResult{}, nil
				}
				if len(calls) == 2 && tc.mode != "all" {
					return &chaoxing.ReserveResult{OK: true}, nil
				}
				return &chaoxing.ReserveResult{Rejected: true}, nil
			})
			if o.State != tc.want || len(calls) != tc.count || !reflect.DeepEqual(calls, []string{"003", "001", "002"}[:tc.count]) {
				t.Fatalf("%+v calls=%v", o, calls)
			}
		})
	}
	n := 0
	_ = TrySeats(context.Background(), []string{"1", "2", "3", "4"}, func(context.Context, string) (*chaoxing.ReserveResult, error) { n++; return nil, nil })
	if n != 0 {
		t.Fatal("four seats sent")
	}
}
func TestCronRecursAcrossDays(t *testing.T) {
	at := time.Date(2030, 1, 1, 7, 0, 0, 0, Zone)
	next, err := Next("0 8 * * *", "", at)
	if err != nil || next.Hour() != 8 {
		t.Fatalf("%v %v", next, err)
	}
	second, err := Next("0 8 * * *", "", next)
	if err != nil || second.Sub(next) != 24*time.Hour {
		t.Fatalf("%v %v", second, err)
	}
	n, err := Next("*/5 * * * * *", "", at)
	if err != nil || n.Sub(at) != 5*time.Second {
		t.Fatalf("%v %v", n, err)
	}
	if _, err = Next("0 0 31 2 *", "", at); err == nil {
		t.Fatal("impossible schedule accepted")
	}
	if _, err = Parse("*/0 * * * *"); err == nil {
		t.Fatal("zero step accepted")
	}
}
func newDB(t *testing.T) *storage.Store {
	t.Helper()
	db, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func TestAtomicClaimAndRestart(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	other, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	now := time.Now()
	j := storage.Job{ID: "one", Next: now, State: "pending", GlobalTime: "08:00:00"}
	if err = db.Add(j); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	execute := func(context.Context, storage.Job) Outcome { calls.Add(1); return Outcome{"succeeded", "ok"} }
	var wg sync.WaitGroup
	for _, store := range []*storage.Store{db, other} {
		wg.Add(1)
		go func(s *storage.Store) {
			defer wg.Done()
			if e := Poll(context.Background(), s, "08:00:00", now, execute); e != nil {
				t.Error(e)
			}
		}(store)
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("sent %d times", calls.Load())
	}
	if err = Poll(context.Background(), db, "08:00:00", now.Add(time.Second), execute); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("repeated after poll")
	}
	records, _ := db.History(j.ID)
	if len(records) != 1 {
		t.Fatalf("history %v", records)
	}
}
func TestRecurringJobAdvancesButUnknownPauses(t *testing.T) {
	now := time.Date(2030, 1, 1, 8, 0, 0, 0, Zone)
	for _, result := range []string{"succeeded", "failed", "unknown", "needs_login"} {
		t.Run(result, func(t *testing.T) {
			db := newDB(t)
			j := storage.Job{ID: result, Next: now, State: "pending", Cron: "0 8 * * *"}
			db.Add(j)
			if err := Poll(context.Background(), db, "08:00:00", now, func(context.Context, storage.Job) Outcome { return Outcome{result, result} }); err != nil {
				t.Fatal(err)
			}
			jobs, _ := db.Jobs()
			want := result
			if result == "failed" || result == "succeeded" {
				want = "pending"
				if jobs[0].Next.Sub(now) != 24*time.Hour {
					t.Fatal(jobs[0].Next)
				}
			}
			if jobs[0].State != want {
				t.Fatal(jobs[0])
			}
		})
	}
}
func TestInterruptedAndMissedNeverSend(t *testing.T) {
	db := newDB(t)
	now := time.Now()
	db.Add(storage.Job{ID: "missed", Next: now.Add(-2 * time.Minute), State: "pending", GlobalTime: "08:00:00"})
	running := storage.Job{ID: "running", Next: now.Add(-time.Hour), State: "pending", GlobalTime: "08:00:00"}
	db.Add(running)
	db.Claim(running, now.Add(-time.Hour))
	count := 0
	if err := Poll(context.Background(), db, "08:00:00", now, func(context.Context, storage.Job) Outcome { count++; return Outcome{} }); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("sent missed/interrupted job")
	}
	jobs, _ := db.Jobs()
	states := map[string]string{}
	for _, j := range jobs {
		states[j.ID] = j.State
	}
	if states["running"] != "unknown" || states["missed"] != "missed" {
		t.Fatal(states)
	}
}
func TestSharedClockReschedulesPending(t *testing.T) {
	db := newDB(t)
	now := time.Date(2030, 1, 1, 7, 0, 0, 0, Zone)
	db.Add(storage.Job{ID: "clock", State: "pending", Next: now.Add(time.Hour), GlobalTime: "08:00:00"})
	err := Poll(context.Background(), db, "09:30:00", now, func(context.Context, storage.Job) Outcome { t.Fatal("early send"); return Outcome{} })
	if err != nil {
		t.Fatal(err)
	}
	jobs, _ := db.Jobs()
	if jobs[0].Next.Hour() != 9 || jobs[0].Next.Minute() != 30 {
		t.Fatal(jobs[0])
	}
}

func TestWorkerDiscoversAllAccountsAndStops(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	stores := []*storage.Store{}
	for _, id := range []string{"张20260001", "李20260002"} {
		cfg, _, err := config.LoadAccount(root, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = config.Save(cfg); err != nil {
			t.Fatal(err)
		}
		dir, _ := storage.AccountDir(root, id)
		db, err := storage.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		stores = append(stores, db)
		if err = db.Add(storage.Job{ID: "same", Next: now, State: "pending", GlobalTime: "08:00:00"}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { Serve(ctx, root); close(done) }()
	deadline := time.Now().Add(4 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		ready = true
		for _, db := range stores {
			j, _ := db.Jobs()
			failure, _ := db.Get("worker_error")
			if len(j) != 1 || j[0].State != "pending" || len(failure) == 0 {
				ready = false
			}
		}
		if ready {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("worker did not process both isolated accounts")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestMonthlyCronUsesRelativeReservationDay(t *testing.T) {
	now := time.Date(2030, 1, 2, 9, 0, 0, 0, Zone)
	j, err := NewJob(6299, "room", "2030-01-03", "19:00", "19:30", []string{"001"}, "0 8 1 * *", "08:00:00", now)
	if err != nil {
		t.Fatal(err)
	}
	if j.DayOffset != 1 || j.Day != "2030-02-02" || j.Next.In(Zone).Format("2006-01-02") != "2030-02-01" {
		t.Fatalf("%+v", j)
	}
}
