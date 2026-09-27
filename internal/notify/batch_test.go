package notify

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"wfuseat/internal/storage"
	"wfuseat/internal/telegram"
)

func batchCfg() BatchConfig {
	return BatchConfig{Settings: cfg(), Window: time.Minute, Delay: 5 * time.Minute}
}
func batchAccount(t *testing.T, root, id string) *storage.Store {
	t.Helper()
	dir, e := storage.AccountDir(root, id)
	if e != nil {
		t.Fatal(e)
	}
	db, e := storage.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func TestBatchCombinesAccountsByPlanMinuteAfterFiveMinutes(t *testing.T) {
	root := t.TempDir()
	journal := dbFor(t)
	at := testNow().Truncate(time.Minute)
	a := batchAccount(t, root, "张20260001")
	b := batchAccount(t, root, "李20260002")
	record(t, a, "a", "succeeded", at)
	record(t, b, "b", "failed", at.Add(40*time.Second))
	record(t, b, "c", "unknown", at.Add(time.Minute))
	sent := []string{}
	send := func(_ context.Context, _ telegram.Settings, m telegram.Message) error {
		s := m.Text
		sent = append(sent, s)
		return nil
	}
	label := func(id string) string { return id }
	for _, n := range []time.Time{at.Add(4*time.Minute + 59*time.Second), at.Add(5 * time.Minute)} {
		if e := PollBatches(context.Background(), root, journal, batchCfg(), n, label, send); e != nil {
			t.Fatal(e)
		}
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "成功/全部：1/2") {
		t.Fatal("batch grouping or timing", len(sent))
	}
	if e := PollBatches(context.Background(), root, journal, batchCfg(), at.Add(6*time.Minute), label, send); e != nil {
		t.Fatal(e)
	}
	if len(sent) != 2 || !strings.Contains(sent[1], "待核实") {
		t.Fatal("different minute merged")
	}
	if e := PollBatches(context.Background(), root, journal, batchCfg(), at.Add(7*time.Minute), label, send); e != nil {
		t.Fatal(e)
	}
	if len(sent) != 2 {
		t.Fatal("resent batch")
	}
}
func TestBatchFrozenRetryAndMissingExecution(t *testing.T) {
	root := t.TempDir()
	journal := dbFor(t)
	at := testNow()
	db := batchAccount(t, root, "张20260001")
	j := storage.Job{ID: "pending", State: "pending", Next: at, Room: "原房间"}
	if e := db.Add(j); e != nil {
		t.Fatal(e)
	}
	c := batchCfg()
	messages := []string{}
	send := func(_ context.Context, _ telegram.Settings, m telegram.Message) error {
		s := m.Text
		messages = append(messages, s)
		if len(messages) == 1 {
			return &telegram.DeliveryError{Message: "限流", RetryAfter: time.Minute}
		}
		return nil
	}
	label := func(id string) string { return id }
	if e := PollBatches(context.Background(), root, journal, c, at.Add(5*time.Minute), label, send); e != nil {
		t.Fatal(e)
	}
	if len(messages) != 1 || !strings.Contains(messages[0], "尚未执行") {
		t.Fatal("missing pending result")
	}
	// A task edited after cutoff must not mutate the persisted notification.
	j.Room = "改后房间"
	if e := db.Reschedule(j); e != nil {
		t.Fatal(e)
	}
	if e := PollBatches(context.Background(), root, journal, c, at.Add(6*time.Minute), label, send); e != nil {
		t.Fatal(e)
	}
	if len(messages) != 2 || messages[0] != messages[1] {
		t.Fatal("retry payload changed")
	}
}
func TestConcurrentBatchWorkersDedupe(t *testing.T) {
	root := t.TempDir()
	journal := dbFor(t)
	at := testNow()
	db := batchAccount(t, root, "张20260001")
	record(t, db, "one", "succeeded", at)
	var count atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := PollBatches(context.Background(), root, journal, batchCfg(), at.Add(5*time.Minute), func(id string) string { return id }, func(context.Context, telegram.Settings, telegram.Message) error { count.Add(1); return nil })
			if e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatal("duplicate", count.Load())
	}
}
func TestBatchNoSecretsAndDisabled(t *testing.T) {
	root := t.TempDir()
	journal := dbFor(t)
	c := batchCfg()
	c.Settings.Enabled = false
	if e := PollBatches(context.Background(), root, journal, c, testNow(), func(string) string { return "" }, func(context.Context, telegram.Settings, telegram.Message) error { t.Fatal("disabled sent"); return nil }); e != nil {
		t.Fatal(e)
	}
	c.Settings.Enabled = true
	c.Window = 10 * time.Minute
	if c.Validate() == nil {
		t.Fatal("delay shorter than group window")
	}
}
