package notify

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"wfuseat/internal/storage"
	"wfuseat/internal/telegram"
)

func cfg() telegram.Settings {
	return telegram.Settings{Enabled: true, BotToken: "123456:" + strings.Repeat("a", 35), ChatID: "123"}
}
func dbFor(t *testing.T) *storage.Store {
	t.Helper()
	db, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func record(t *testing.T, db *storage.Store, id, state string, now time.Time) {
	t.Helper()
	j := storage.Job{ID: id, Room: "测试自习室", Day: now.AddDate(0, 0, 1).Format("2006-01-02"), Start: "08:00", End: "09:00", Seats: []string{"001"}, State: "pending", Next: now}
	if e := db.Add(j); e != nil {
		t.Fatal(e)
	}
	if ok, e := db.Claim(j, now); e != nil || !ok {
		t.Fatal("claim", e)
	}
	// Simulate a recurring job advancing; the report must retain the old date.
	j.Next = now.AddDate(0, 0, 1)
	j.Day = j.Next.AddDate(0, 0, 1).Format("2006-01-02")
	j.State = "pending"
	if e := db.Finish(j, now, state); e != nil {
		t.Fatal(e)
	}
}
func TestDailyAggregationAndDurableDedupe(t *testing.T) {
	dir := t.TempDir()
	db, e := storage.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	now := testNow()
	record(t, db, "a", "succeeded", now)
	record(t, db, "b", "unknown", now)
	sent := 0
	send := func(_ context.Context, c telegram.Settings, text string) error {
		sent++
		if !strings.Contains(text, "成功 1") || !strings.Contains(text, "待核实 1") || !strings.Contains(text, now.AddDate(0, 0, 1).Format("2006-01-02")) {
			t.Fatal("report missing result")
		}
		return nil
	}
	if e = Poll(context.Background(), db, cfg(), time.Now(), send); e != nil {
		t.Fatal(e)
	}
	if sent != 0 {
		t.Fatal("sent before quiet period")
	}
	// Add a later same-day job: don't send early while it still waits.
	j := storage.Job{ID: "later", State: "pending", Next: now.Add(10 * time.Second)}
	if e = db.Add(j); e != nil {
		t.Fatal(e)
	}
	if e = Poll(context.Background(), db, cfg(), now.Add(2*time.Minute), send); e != nil {
		t.Fatal(e)
	}
	if sent != 0 {
		t.Fatal("sent before later job")
	}
	if e = db.Cancel(j.ID); e != nil {
		t.Fatal(e)
	}
	if e = Poll(context.Background(), db, cfg(), now.Add(2*time.Minute), send); e != nil {
		t.Fatal(e)
	}
	db.Close()
	db, e = storage.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = Poll(context.Background(), db, cfg(), now.Add(3*time.Minute), send); e != nil {
		t.Fatal(e)
	}
	if sent != 1 {
		t.Fatalf("deliveries %d", sent)
	}
}
func TestConcurrentWorkersAndAccountIsolation(t *testing.T) {
	root := t.TempDir()
	now := testNow()
	a, e := storage.Open(filepath.Join(root, "a"))
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	a2, e := storage.Open(filepath.Join(root, "a"))
	if e != nil {
		t.Fatal(e)
	}
	defer a2.Close()
	b, e := storage.Open(filepath.Join(root, "b"))
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	record(t, a, "same", "succeeded", now)
	record(t, b, "same", "failed", now)
	var count atomic.Int32
	send := func(context.Context, telegram.Settings, string) error { count.Add(1); return nil }
	var wg sync.WaitGroup
	for _, db := range []*storage.Store{a, a2, b} {
		wg.Add(1)
		go func(db *storage.Store) {
			defer wg.Done()
			if e := Poll(context.Background(), db, cfg(), now.Add(2*time.Minute), send); e != nil {
				t.Error(e)
			}
		}(db)
	}
	wg.Wait()
	if count.Load() != 2 {
		t.Fatalf("cross account or duplicate: %d", count.Load())
	}
}
func TestDeliveryRetryOnlyExplicitRateLimit(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		db := dbFor(t)
		now := testNow()
		record(t, db, "one", "failed", now)
		count := 0
		send := func(context.Context, telegram.Settings, string) error {
			count++
			if ambiguous {
				return errors.New("network uncertain")
			}
			if count == 1 {
				return &telegram.DeliveryError{Message: "限流", RetryAfter: time.Minute}
			}
			return nil
		}
		for _, delta := range []time.Duration{2 * time.Minute, 2*time.Minute + time.Second, 4 * time.Minute} {
			if e := Poll(context.Background(), db, cfg(), now.Add(delta), send); e != nil {
				t.Fatal(e)
			}
		}
		want := 2
		if ambiguous {
			want = 1
		}
		if count != want {
			t.Fatalf("retry count %d", count)
		}
	}
}
func TestCrashClaimAndRecovery(t *testing.T) {
	db := dbFor(t)
	now := testNow()
	j := storage.Job{ID: "interrupted", State: "pending", Next: now, Day: now.Format("2006-01-02")}
	if e := db.Add(j); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Claim(j, now); e != nil {
		t.Fatal(e)
	}
	if e := db.Recover(now.Add(6 * time.Minute)); e != nil {
		t.Fatal(e)
	}
	runs, e := db.Outcomes(now.Add(-time.Second))
	if e != nil || len(runs) != 1 || runs[0].State != "unknown" {
		t.Fatal("lost unknown outcome")
	}
	day := now.Format("2006-01-02")
	if ok, e := db.ClaimDelivery(day, now); e != nil || !ok {
		t.Fatal("delivery claim")
	}
	if e := Poll(context.Background(), db, cfg(), now.Add(8*time.Minute), func(context.Context, telegram.Settings, string) error { t.Fatal("ambiguous claim resent"); return nil }); e != nil {
		t.Fatal(e)
	}
}

func testNow() time.Time {
	n := time.Now().In(zone)
	return time.Date(n.Year(), n.Month(), n.Day()+1, 12, 0, 0, 0, zone)
}
func TestPendingOnlyDayEndReport(t *testing.T) {
	db := dbFor(t)
	now := testNow()
	j := storage.Job{ID: "never-ran", Room: "房间", State: "pending", Next: now, Day: now.AddDate(0, 0, 1).Format("2006-01-02")}
	if e := db.Add(j); e != nil {
		t.Fatal(e)
	}
	count := 0
	send := func(_ context.Context, _ telegram.Settings, text string) error {
		count++
		if !strings.Contains(text, "尚未执行") {
			t.Fatal("unexecuted task mislabeled")
		}
		return nil
	}
	if e := Poll(context.Background(), db, cfg(), now.Add(time.Hour), send); e != nil {
		t.Fatal(e)
	}
	if count != 0 {
		t.Fatal("premature pending report")
	}
	if e := Poll(context.Background(), db, cfg(), now.Add(12*time.Hour), send); e != nil {
		t.Fatal(e)
	}
	if count != 1 {
		t.Fatal("missing day-end report")
	}
}

func TestQuietMinute(t *testing.T) {
	db := dbFor(t)
	now := time.Now().In(zone)
	record(t, db, "quiet", "succeeded", now)
	if e := Poll(context.Background(), db, cfg(), now, func(context.Context, telegram.Settings, string) error {
		t.Fatal("sent before quiet minute")
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
