package notify

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"
	"wfuseat/internal/storage"
	"wfuseat/internal/telegram"
)

type BatchConfig struct {
	Proxy    string
	Settings telegram.Settings
	Window   time.Duration
	Delay    time.Duration
}

func (c BatchConfig) Validate() error {
	if !c.Settings.Enabled {
		return nil
	}
	if _, e := telegram.NewSenderWithProxy(c.Proxy); e != nil {
		return e
	}
	if e := c.Settings.Validate(); e != nil {
		return e
	}
	if c.Settings.BotToken == "" {
		return errors.New("后端 Telegram Bot Token 缺失")
	}
	if c.Window < time.Minute || c.Window > time.Hour || c.Window%time.Minute != 0 {
		return errors.New("Telegram 分批窗口需要 1–60 分钟的整数分钟")
	}
	if c.Delay < c.Window || c.Delay > 24*time.Hour {
		return errors.New("Telegram 推送延迟需不少于分批窗口，且不超过 24 小时")
	}
	return nil
}

type BatchSend func(context.Context, telegram.Settings, telegram.Message) error

type BatchEntry struct {
	Account string
	Item    telegram.Item
}
type Batch struct {
	At      time.Time
	Entries []BatchEntry
}

// CollectBatch uses planned occurrence times; execution delays never split a batch.
// The label callback is server-owned and cannot be set to another identity by clients.
func CollectBatch(root string, c BatchConfig, now time.Time, label func(string) string) ([]Batch, error) {
	ids, e := storage.Accounts(root)
	if e != nil {
		return nil, e
	}
	grouped := map[int64]*Batch{}
	blocked := map[int64]bool{}
	since := now.AddDate(0, 0, -7)
	for _, id := range ids {
		if !storage.IsIdentifiedAccount(id) && !storage.IsServiceAccount(id) {
			continue
		}
		dir := filepath.Join(root, "accounts", id)
		if _, e = os.Stat(filepath.Join(dir, "state.db")); os.IsNotExist(e) {
			continue
		}
		// AccountDir enforces the existing no-symlink boundary.
		dir, e = storage.AccountDir(root, id)
		if e != nil {
			return nil, e
		}
		db, e := storage.Open(dir)
		if e != nil {
			return nil, e
		}
		if e = db.Recover(now); e != nil {
			db.Close()
			return nil, e
		}
		runs, e := db.Outcomes(since)
		if e != nil {
			db.Close()
			return nil, e
		}
		jobs, e := db.Jobs()
		db.Close()
		if e != nil {
			return nil, e
		}
		seen := map[string]bool{}
		name := label(id)
		if name == "" {
			continue
		} // Deleted identities are excluded by the server.
		add := func(j storage.Job, at time.Time, state, detail string) {
			if at.IsZero() || at.Before(since) || at.After(now) {
				return
			}
			start := at.In(zone).Truncate(c.Window)
			if now.Before(start.Add(c.Delay)) {
				return
			}
			key := start.UnixMilli()
			if state == "running" {
				blocked[key] = true
			}
			b := grouped[key]
			if b == nil {
				b = &Batch{At: start}
				grouped[key] = b
			}
			day := j.Day
			if j.Cron != "" {
				day = at.In(zone).AddDate(0, 0, j.DayOffset).Format("2006-01-02")
			}
			b.Entries = append(b.Entries, BatchEntry{Account: name, Item: telegram.Item{Room: j.Room, Day: day, Start: j.Start, End: j.End, Seats: j.Seats, State: state, Detail: detail}})
		}
		for _, r := range runs {
			seen[fmt.Sprintf("%s/%d", r.Job.ID, r.At.UnixMilli())] = true
			add(r.Job, r.At, r.State, r.Message)
		}
		for _, j := range jobs {
			if seen[fmt.Sprintf("%s/%d", j.ID, j.Next.UnixMilli())] {
				continue
			}
			switch j.State {
			case "pending":
				add(j, j.Next, "not_run", "")
			case "running":
				add(j, j.Next, "running", "")
			}
		}
	}
	out := make([]Batch, 0, len(grouped))
	for key, b := range grouped {
		if blocked[key] {
			continue
		}
		sort.SliceStable(b.Entries, func(i, j int) bool {
			a, z := b.Entries[i], b.Entries[j]
			if a.Account != z.Account {
				return a.Account < z.Account
			}
			if a.Item.Day != z.Item.Day {
				return a.Item.Day < z.Item.Day
			}
			if a.Item.Start != z.Item.Start {
				return a.Item.Start < z.Item.Start
			}
			return a.Item.Room < z.Item.Room
		})
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

func PollBatches(ctx context.Context, root string, journal *storage.Store, c BatchConfig, now time.Time, label func(string) string, send BatchSend) error {
	if !c.Settings.Enabled {
		return nil
	}
	if e := c.Validate(); e != nil {
		return e
	}
	batches, e := CollectBatch(root, c, now, label)
	if e != nil {
		return e
	}
	// Changing the receiving chat creates an independent delivery ledger.
	digest := sha256.Sum256([]byte(c.Settings.ChatID))
	prefix := fmt.Sprintf("batch-%x/", digest[:8])
	retryAt, e := journal.DeliveryRetryAfter(prefix)
	if e != nil {
		return e
	}
	if now.Before(retryAt) {
		return nil
	}

	saved, e := journal.Batches(prefix, now.AddDate(0, 0, -7))
	if e != nil {
		return e
	}
	for _, b := range batches {
		key := prefix + b.At.Format("20060102T1504")
		if _, ok := saved[key]; ok {
			continue
		}
		rows := make([]telegram.BatchItem, 0, len(b.Entries))
		for _, v := range b.Entries {
			rows = append(rows, telegram.BatchItem{Account: v.Account, Item: v.Item})
		}
		full := telegram.RenderBatch(b.At, c.Settings, rows)
		summaries := telegram.CompactBatch(rows)
		messages := []telegram.Message{}
		for _, summary := range summaries {
			m, e := SaveReport(journal, c.Settings.ChatID, summary, full, now)
			if e != nil {
				return e
			}
			messages = append(messages, m)
		}
		raw, e := json.Marshal(messages)
		if e != nil {
			return e
		}
		if e = journal.FreezeBatch(key, raw, b.At); e != nil {
			return e
		}
	}
	saved, e = journal.Batches(prefix, now.AddDate(0, 0, -7))
	if e != nil {
		return e
	}

	keys := make([]string, 0, len(saved))
	for k := range saved {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		var pages []telegram.Message
		if e = json.Unmarshal(saved[key], &pages); e != nil {
			return e
		}
		for i, text := range pages {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			pageKey := fmt.Sprintf("%s/%04d", key, i+1)
			claimed, e := journal.ClaimDelivery(pageKey, now)
			if e != nil {
				return e
			}
			if !claimed {
				continue
			}
			state, note, next := "sent", "已送达", time.Time{}
			if e = send(ctx, c.Settings, text); e != nil {
				state, note = "unknown", "未确认送达，请核实聊天记录"
				var de *telegram.DeliveryError
				if errors.As(e, &de) {
					note = de.Message
					if de.RetryAfter > 0 {
						state = "retry"
						next = now.Add(de.RetryAfter)
					}
				}
			}
			if e = journal.FinishDelivery(pageKey, state, note, next); e != nil {
				return e
			}
			log.Printf("Telegram 批次 %s 第 %d/%d 页：%s", key[len(prefix):], i+1, len(pages), note)
			// Do not continue flooding the same chat after explicit rate limiting.
			if state == "retry" {
				return nil
			}
		}
	}
	return nil
}
func ServeBatches(ctx context.Context, root string, journal *storage.Store, c BatchConfig, label func(string) string) {
	sender, e := telegram.NewSenderWithProxy(c.Proxy)
	if e != nil {
		log.Print("Telegram 网络配置无效")
		return
	}
	defer sender.Client.CloseIdleConnections()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	poll := func() {
		if e := PollBatches(ctx, root, journal, c, time.Now(), label, sender.SendMessage); e != nil && ctx.Err() == nil {
			log.Print("Telegram 批量推送暂不可用，请检查通知数据库或配置")
		}
	}
	poll()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			poll()
		}
	}
}
