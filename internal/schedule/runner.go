// Package schedule executes durable account-scoped jobs. It never reuses the
// TUI's mutable client, so room/date navigation cannot change an execution.
package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"wfuseat/internal/chaoxing"
	"wfuseat/internal/config"
	"wfuseat/internal/storage"
)

type Outcome struct{ State, Message string }
type Executor func(context.Context, storage.Job) Outcome

// TrySeats stops on success or ambiguity; only an explicit refusal advances.
func TrySeats(ctx context.Context, seats []string, send func(context.Context, string) (*chaoxing.ReserveResult, error)) Outcome {
	if len(seats) < 1 || len(seats) > 3 {
		return Outcome{"failed", "候选座位必须为 1–3 个"}
	}
	seen := map[string]bool{}
	for _, s := range seats {
		if s == "" || seen[s] {
			return Outcome{"failed", "候选座位重复或为空"}
		}
		seen[s] = true
	}
	var notes []string
	for _, seat := range seats {
		if ctx.Err() != nil {
			return Outcome{"unknown", "执行被中断，需人工核实"}
		}
		r, err := send(ctx, seat)
		if err != nil {
			return Outcome{"unknown", strings.Join(append(notes, seat+" 结果未知，已停止后续请求"), "；")}
		}
		if r == nil {
			return Outcome{"unknown", "服务端结果缺失"}
		}
		if r.OK {
			return Outcome{"succeeded", strings.Join(append(notes, seat+" 预约成功"), "；")}
		}
		if !r.Rejected {
			return Outcome{"unknown", strings.Join(append(notes, seat+" 结果未确认，已暂停"), "；")}
		}
		notes = append(notes, seat+" 被拒绝："+r.Message)
	}
	return Outcome{"failed", strings.Join(notes, "；")}
}
func Execute(ctx context.Context, cfg config.Config, db *storage.Store, j storage.Job) Outcome {
	if !cfg.AllowSubmit || !db.Enabled() {
		return Outcome{"paused", "账号已关闭提交"}
	}
	login, err := chaoxing.NewQRLogin(cfg.Proxy, cfg.LogDir)
	if err != nil {
		return Outcome{"failed", "无法创建独立登录会话"}
	}
	defer login.Close()
	return ExecuteWithLogin(ctx, cfg, db, j, login)
}

// ExecuteWithLogin accepts an isolated session, including an offline transport for tests.
func ExecuteWithLogin(ctx context.Context, cfg config.Config, db *storage.Store, j storage.Job, login *chaoxing.QRLogin) Outcome {
	cfg.Normalize()
	if cfg.UserAgent != "" {
		login.Session.UserAgent = cfg.UserAgent
	}
	if !cfg.AllowSubmit || !db.Enabled() {
		return Outcome{"paused", "账号已关闭提交"}
	}
	raw, err := db.Get("cookies")
	if err != nil || len(raw) == 0 {
		return Outcome{"needs_login", "请先为此账号扫码登录"}
	}
	if err = login.Session.AttachCookies(raw, func(b []byte) error { return db.Put("cookies", b) }); err != nil {
		return Outcome{"needs_login", "保存的登录状态损坏，请重新登录"}
	}
	delay, e := fixedDelayMS(cfg.DelayMS)
	if e != nil {
		return Outcome{"failed", e.Error()}
	}
	audit := map[string]any{"job_id": j.ID, "planned_time": j.Next.Format(time.RFC3339Nano), "delay_mode": "fixed", "configured_delay_ms": cfg.DelayMS, "delay_ms": delay, "delay_started_at": time.Now().Format(time.RFC3339Nano)}
	b, _ := json.Marshal(audit)
	if e = db.Put("last_delay/"+j.ID, b); e != nil {
		return Outcome{"failed", "延迟日志保存失败，未提交"}
	}
	if login.Session.Log != nil {
		login.Session.Log.Event("reservation_delay", audit)
	}
	if e = waitDelay(ctx, delay); e != nil {
		return Outcome{"paused", "固定延迟等待被取消，未提交"}
	}
	if !db.Enabled() {
		return Outcome{"paused", "账号预订已暂停"}
	}
	client := chaoxing.NewSeatClient(login, cfg.FIDEnc, cfg.MappID)
	client.SetReserveConfig(chaoxing.ReserveConfig{SubmitURL: cfg.SubmitURL, Method: cfg.SubmitMethod})
	if _, err = client.OpenHome(ctx); err != nil {
		return Outcome{"needs_login", "登录校验失败，请重新登录后恢复任务"}
	}
	if storage.IsServiceAccount(cfg.Account) && (client.StudentID() == "" || storage.ServiceAccountID(cfg.FIDEnc, client.StudentID()) != cfg.Account) {
		return Outcome{"needs_login", "学校身份与任务所属账号不一致，未提交"}
	}
	day := j.Day
	if j.Cron != "" {
		day = j.Next.In(Zone).AddDate(0, 0, j.DayOffset).Format("2006-01-02")
	}
	if _, err = client.ListRooms(ctx, day); err != nil {
		return Outcome{"failed", "房间查询失败，未提交"}
	}
	if _, err = client.OpenRoom(ctx, j.RoomID); err != nil {
		return Outcome{"failed", "房间打开失败，未提交"}
	}
	if status, _, ok := client.ReserveWindow(); !ok || status != "AVAILABLE" {
		return Outcome{"failed", "预约窗口尚未开放，未提交"}
	}
	if !client.CanSign() {
		return Outcome{"failed", "执行时未取得新的签名参数，未提交"}
	}
	// Validate time and membership once, before the first write.
	if err = client.ValidatePlan(j.Seats, j.Start, j.End); err != nil {
		return Outcome{"failed", err.Error()}
	}
	targets := client.ReserveTargets()
	if !targets.HasSubmit {
		return Outcome{"failed", "选座页及其脚本未提供可确认的预约接口，未提交"}
	}
	return TrySeats(ctx, j.Seats, func(ctx context.Context, seat string) (*chaoxing.ReserveResult, error) {
		if _, err := client.Plan(seat, j.Start, j.End); err != nil {
			return nil, err
		}
		if !db.Enabled() {
			return nil, errors.New("账号预订已暂停")
		}
		return client.SubmitReservation(ctx)
	})
}

// Poll performs atomic claims. The occurrence is durable before its first request.
func Poll(ctx context.Context, db *storage.Store, clock string, now time.Time, execute Executor) error {
	if err := db.Recover(now); err != nil {
		return err
	}
	jobs, err := db.Jobs()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if wall := time.Now(); wall.After(now) {
			now = wall
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if j.State != "pending" || j.AwaitWindow {
			continue
		}
		if j.Cron == "" && clock != "" && !j.RoomWindow && j.GlobalTime != clock {
			j.Next, err = Next("", clock, now)
			if err != nil {
				return err
			}
			j.GlobalTime = clock
			if err = db.Reschedule(j); err != nil {
				return err
			}
		}
		if j.Next.After(now) {
			continue
		}
		claimed, err := db.Claim(j, now)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		occurrence := j.Next
		result := Outcome{"missed", "程序未在计划时间运行；本次已跳过"}
		if now.Sub(occurrence) <= time.Minute {
			runCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			result = execute(runCtx, j)
			cancel()
		}
		j.Result = result.Message
		if Recurring(j) {
			j.Result = j.Day + " · " + j.Result
		}
		j.State = result.State
		if Recurring(j) && (result.State == "succeeded" || result.State == "failed" || result.State == "missed") {
			advanceDaily(&j, now)
		}
		if j.Cron != "" && (result.State == "succeeded" || result.State == "failed" || result.State == "missed") {
			after := now
			if realNow := time.Now(); realNow.After(after) {
				after = realNow
			}
			j.Next, err = Next(j.Cron, clock, after)
			if err != nil {
				j.State = "paused"
				j.Result = "cron 无后续匹配"
			} else {
				j.State = "pending"
			}
		}
		if err = db.Finish(j, occurrence); err != nil {
			return err
		}
	}
	return nil
}

// Serve discovers account directories; each account runs independently. SQLite
// claims prevent double sends even if a TUI and a headless worker coexist.
func Serve(ctx context.Context, root string) {
	running := map[string]bool{}
	ended := make(chan string, 32)
	var wg sync.WaitGroup
	defer wg.Wait()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		ids, _ := storage.Accounts(root)
		for _, id := range ids {
			if !storage.IsIdentifiedAccount(id) && !storage.IsServiceAccount(id) {
				continue
			}
			if running[id] {
				continue
			}
			running[id] = true
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				serveAccount(ctx, root, id)
				select {
				case ended <- id:
				case <-ctx.Done():
				}
			}(id)
		}
		select {
		case <-ctx.Done():
			return
		case id := <-ended:
			delete(running, id)
		case <-ticker.C:
		}
	}
}
func serveAccount(ctx context.Context, root, id string) {
	dir, err := storage.AccountDir(root, id)
	if err != nil {
		return
	}
	db, err := storage.Open(dir)
	if err != nil {
		return
	}
	defer db.Close()
	var refreshed, retryAt time.Time
	windowReady := false
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			if _, e := os.Stat(filepath.Join(dir, "config.json")); os.IsNotExist(e) {
				return
			}
			cfg, _, err := config.LoadAccount(root, id)
			if err != nil {
				continue
			}
			if _, e := os.Stat(filepath.Join(dir, "config.json")); os.IsNotExist(e) {
				return
			}
			if !cfg.AllowSubmit || !db.Enabled() {
				continue
			}
			if now.Before(retryAt) {
				continue
			}
			if refreshed.IsZero() || now.Sub(refreshed) > 5*time.Minute || awaitingDailyWindow(db, now) {
				if e := RefreshWindows(ctx, cfg, db, now); e != nil {
					windowReady = false
					retryAt = now.Add(30 * time.Second)
					_ = db.Put("worker_error", []byte("开放时间获取失败，暂停执行："+e.Error()))
					continue
				}
				refreshed = now
				windowReady = true
				_ = db.Delete("worker_error")
			}
			if !windowReady {
				continue
			}
			clock := ""

			err = Poll(ctx, db, clock, now, func(c context.Context, j storage.Job) Outcome { return Execute(c, cfg, db, j) })
			if err != nil && !errors.Is(err, context.Canceled) {
				_ = db.Put("worker_error", []byte("任务存储操作失败"))
			}
		}
	}
}
func NewJob(roomID int, room, day, start, end string, seats []string, spec, clock string, now time.Time) (storage.Job, error) {
	if roomID <= 0 || len(seats) == 0 || len(seats) > 3 {
		return storage.Job{}, errors.New("请选择房间与 1–3 个候选座位")
	}
	a, e1 := time.Parse("15:04", start)
	b, e2 := time.Parse("15:04", end)
	if e1 != nil || e2 != nil || !b.After(a) {
		return storage.Job{}, errors.New("请选择有效的预约时段")
	}
	next, err := Next(spec, clock, now)
	if err != nil {
		return storage.Job{}, err
	}
	anchor := next
	if spec != "" {
		anchor = now
	}
	offset, err := Offset(day, anchor)
	if err != nil {
		return storage.Job{}, err
	}
	if offset < 0 || offset > 6 {
		return storage.Job{}, errors.New("一次性预约日期需位于执行日起七天内；循环任务请选择今天起七天内的日期作为相对日模板")
	}
	if spec == "" {
		endAt, _ := time.ParseInLocation("2006-01-02 15:04", day+" "+end, Zone)
		if !next.Before(endAt) {
			return storage.Job{}, errors.New("执行时间晚于预约结束时间")
		}
	}
	if spec != "" {
		day = next.In(Zone).AddDate(0, 0, offset).Format("2006-01-02")
	}
	return storage.Job{ID: fmt.Sprintf("%d", now.UnixNano()), RoomID: roomID, Room: room, Day: day, Start: start, End: end, Seats: append([]string(nil), seats...), Cron: spec, DayOffset: offset, State: "pending", Next: next, Created: now, GlobalTime: clock}, nil
}

// ExportJobs provides an account-scoped view for the CLI, without credentials.
func ExportJobs(root, id string) ([]byte, error) {
	dir, err := storage.AccountDir(root, id)
	if err != nil {
		return nil, err
	}
	db, err := storage.Open(dir)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	jobs, err := db.Jobs()
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(jobs, "", "  ")
}

// RefreshWindows runs before dispatch on every process start and refreshes per-room dates.
func RefreshWindows(ctx context.Context, cfg config.Config, db *storage.Store, now time.Time) error {
	jobs, e := db.Jobs()
	if e != nil {
		return e
	}
	pending := false
	for _, j := range jobs {
		if j.State == "pending" && j.Cron == "" && windowInHorizon(j, now) {
			pending = true
		}
	}
	if !pending {
		return nil
	}
	l, e := chaoxing.NewQRLogin(cfg.Proxy, cfg.LogDir)
	if e != nil {
		return e
	}
	defer l.Close()
	cfg.Normalize()
	if cfg.UserAgent != "" {
		l.Session.UserAgent = cfg.UserAgent
	}
	raw, e := db.Get("cookies")
	if e != nil || len(raw) == 0 {
		return errors.New("凭证缺失")
	}
	if e = l.Session.AttachCookies(raw, func(b []byte) error { return db.Put("cookies", b) }); e != nil {
		return e
	}
	c := chaoxing.NewSeatClient(l, cfg.FIDEnc, cfg.MappID)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if _, e = c.OpenHome(ctx); e != nil {
		return e
	}
	if storage.IsServiceAccount(cfg.Account) && (c.StudentID() == "" || storage.ServiceAccountID(cfg.FIDEnc, c.StudentID()) != cfg.Account) {
		return errors.New("学校身份与任务所属账号不一致")
	}
	return refreshWindowJobs(db, jobs, now, func(j storage.Job) (time.Time, error) { return c.QueryOpening(ctx, j.RoomID, j.Day) })
}
func refreshWindowJobs(db *storage.Store, jobs []storage.Job, now time.Time, lookup func(storage.Job) (time.Time, error)) error {
	for _, j := range jobs {
		if j.State != "pending" || j.Cron != "" || !windowInHorizon(j, now) {
			continue
		}
		expected := j.Next
		if j.AwaitWindow && Recurring(j) {
			end, err := time.ParseInLocation("2006-01-02 15:04", j.Day+" "+j.End, Zone)
			if err != nil {
				return err
			}
			if !end.After(now) {
				j.Result = j.Day + " · 未运行期间已过期，未补发"
				advanceDaily(&j, now)
				if e := db.RescheduleOccurrence(j, expected); e != nil {
					return e
				}
				continue
			}
		}
		at, e := lookup(j)
		if e != nil {
			return e
		}
		if at.IsZero() {
			if j.RoomWindow {
				continue
			}
			at = now.Add(2 * time.Second)
		}
		j.Next = at
		j.AwaitWindow = false
		j.RoomWindow = true
		j.GlobalTime = ""
		if e = db.RescheduleOccurrence(j, expected); e != nil {
			return e
		}
	}
	return nil
}
