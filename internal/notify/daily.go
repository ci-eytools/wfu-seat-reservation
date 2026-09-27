// Package notify sends summaries independently of reservation timing.
package notify

import (
	"context"
	"errors"
	"sort"
	"time"
	"wfuseat/internal/config"
	"wfuseat/internal/storage"
	"wfuseat/internal/telegram"
)

var zone = time.FixedZone("Asia/Shanghai", 8*3600)

type Send func(context.Context, telegram.Settings, string) error

func Serve(ctx context.Context, root, id string, db *storage.Store) {
	sender := telegram.NewSender()
	defer sender.Client.CloseIdleConnections()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			cfg, _, e := config.LoadAccount(root, id)
			if e != nil {
				continue
			}
			if e = Poll(ctx, db, cfg.Telegram, now, sender.Send); e != nil {
				_ = db.Put("telegram_status", []byte("推送记录读取或保存失败"))
			}
		}
	}
}

// Summaries cover execution dates, not the booked dates. Wait for all known jobs
// of the day and a quiet minute. On restart, catch up at most the past seven days.
func Poll(ctx context.Context, db *storage.Store, cfg telegram.Settings, now time.Time, send Send) error {
	if !cfg.Enabled {
		return nil
	}
	if e := cfg.Validate(); e != nil {
		return e
	}
	if e := db.Recover(now); e != nil {
		return e
	}
	today := now.In(zone).Format("2006-01-02")
	runs, e := db.Outcomes(now.AddDate(0, 0, -7))
	if e != nil {
		return e
	}
	jobs, e := db.Jobs()
	if e != nil {
		return e
	}
	grouped := map[string][]telegram.Item{}
	blocked := map[string]bool{}
	seen := map[string]bool{}
	for _, r := range runs {
		seen[r.Job.ID+"/"+r.At.Format(time.RFC3339Nano)] = true
		day := r.At.In(zone).Format("2006-01-02")
		if r.State == "running" || now.Sub(r.Finished) < time.Minute {
			blocked[day] = true
		}
		bookingDay := r.Job.Day
		if r.Job.Cron != "" {
			bookingDay = r.At.In(zone).AddDate(0, 0, r.Job.DayOffset).Format("2006-01-02")
		}
		grouped[day] = append(grouped[day], telegram.Item{Room: r.Job.Room, Day: bookingDay, Start: r.Job.Start, End: r.Job.End, Seats: r.Job.Seats, State: r.State})
	}
	for _, j := range jobs {
		if j.State != "pending" && j.State != "running" {
			continue
		}
		if j.Next.IsZero() || j.Next.Before(now.AddDate(0, 0, -7)) {
			continue
		}
		day := j.Next.In(zone).Format("2006-01-02")
		if day > today {
			continue
		}
		if j.State == "running" {
			blocked[day] = true
			continue
		}
		if day == today {
			blocked[day] = true
			continue
		}
		if seen[j.ID+"/"+j.Next.Format(time.RFC3339Nano)] {
			continue
		}
		grouped[day] = append(grouped[day], telegram.Item{Room: j.Room, Day: j.Day, Start: j.Start, End: j.End, Seats: j.Seats, State: "not_run"})
	}

	days := make([]string, 0, len(grouped))
	for day := range grouped {
		days = append(days, day)
	}
	sort.Strings(days)
	for _, day := range days {
		if blocked[day] || day > today {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ok, e := db.ClaimDelivery(day, now)
		if e != nil {
			return e
		}
		if !ok {
			continue
		}
		state, note, next := "sent", "已送达", time.Time{}
		e = send(ctx, cfg, telegram.Render(day, cfg, grouped[day]))
		if e != nil {
			state, note = "unknown", "未确认送达，请检查 Telegram 聊天记录"
			var de *telegram.DeliveryError
			if errors.As(e, &de) {
				note = de.Message
				if de.RetryAfter > 0 {
					state = "retry"
					next = now.Add(de.RetryAfter)
				}
			}
		}
		if e = db.FinishDelivery(day, state, note, next); e != nil {
			return e
		}
		if e = db.Put("telegram_status", []byte(day+" · "+note)); e != nil {
			return e
		}
	}
	status, e := db.DeliveryStatus()
	if e != nil {
		return e
	}
	return db.Put("telegram_status", []byte(status))
}
