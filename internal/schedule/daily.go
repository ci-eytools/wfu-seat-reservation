package schedule

import (
	"fmt"
	"time"
	"wfuseat/internal/storage"
)

// Daily ranges count calendar dates; skipped weekends do not extend the range.
func ConfigureDaily(j *storage.Job, days int, skipWeekends bool) error {
	if days < 1 || days > 366 {
		return fmt.Errorf("循环天数须为 1–366")
	}
	start, err := time.ParseInLocation("2006-01-02", j.Day, Zone)
	if err != nil {
		return err
	}
	until := start.AddDate(0, 0, days-1)
	first := start
	for skipWeekends && weekend(first) {
		first = first.AddDate(0, 0, 1)
	}
	if first.After(until) {
		return fmt.Errorf("所选日期范围全部为周末，请增加循环天数或关闭跳过周末")
	}
	j.Cron = ""
	j.RepeatStart = start.Format("2006-01-02")
	j.RepeatUntil = until.Format("2006-01-02")
	j.SkipWeekends = skipWeekends
	if first.After(start) {
		setDailyDate(j, first)
	}
	return nil
}
func weekend(t time.Time) bool { return t.Weekday() == time.Saturday || t.Weekday() == time.Sunday }
func setDailyDate(j *storage.Job, day time.Time) {
	j.Day = day.Format("2006-01-02")
	if len(j.WeekPlan) == 7 {
		p := j.WeekPlan[weekdayIndex(day)]
		j.Start = p.Start
		j.End = p.End
	}
	j.Next = day // Placeholder only; dispatch must wait for the actual room window.
	j.RoomWindow = false
	j.AwaitWindow = true
	j.GlobalTime = ""
	j.State = "pending"
}
func advanceDaily(j *storage.Job, now time.Time) {
	day, err := time.ParseInLocation("2006-01-02", j.Day, Zone)
	if err != nil {
		j.State = "paused"
		j.Result += "；循环日期无效"
		return
	}
	until, err := time.ParseInLocation("2006-01-02", j.RepeatUntil, Zone)
	if j.RepeatForever {
		until = now.In(Zone).AddDate(0, 0, 8)
		if until.Before(day) {
			until = day.AddDate(0, 0, 8)
		}
		err = nil
	}
	if err != nil {
		j.State = "paused"
		j.Result += "；循环截止日期无效"
		return
	}
	for day = day.AddDate(0, 0, 1); !day.After(until); day = day.AddDate(0, 0, 1) {
		if !activeOn(*j, day) {
			continue
		}
		end, _ := time.ParseInLocation("2006-01-02 15:04", day.Format("2006-01-02")+" "+endOn(*j, day), Zone)
		if !end.After(now) {
			continue
		}
		setDailyDate(j, day)
		return
	}
	j.State = "completed"
	j.AwaitWindow = false
}
func windowInHorizon(j storage.Job, now time.Time) bool {
	if !Recurring(j) {
		return true
	}
	return j.Day <= now.In(Zone).AddDate(0, 0, 6).Format("2006-01-02")
}
func awaitingDailyWindow(db *storage.Store, now time.Time) bool {
	jobs, err := db.Jobs()
	if err != nil {
		return false
	}
	for _, j := range jobs {
		if j.State == "pending" && j.AwaitWindow && windowInHorizon(j, now) {
			return true
		}
	}
	return false
}

func weekdayIndex(day time.Time) int { return (int(day.Weekday()) + 6) % 7 }
func activeOn(j storage.Job, day time.Time) bool {
	if len(j.WeekPlan) == 7 {
		return j.WeekPlan[weekdayIndex(day)].Enabled
	}
	return !j.SkipWeekends || !weekend(day)
}
func endOn(j storage.Job, day time.Time) string {
	if len(j.WeekPlan) == 7 {
		return j.WeekPlan[weekdayIndex(day)].End
	}
	return j.End
}

// NewAutomaticJob stores the exact weekly rule shown in the review. No provisional
// wall-clock schedule is dispatchable until the room window has been fetched.
func NewAutomaticJob(roomID int, room string, seats []string, startDay string, days int, mode string, plan []storage.DayPlan, now time.Time) (storage.Job, error) {
	if roomID <= 0 || len(seats) < 1 || len(seats) > 3 {
		return storage.Job{}, fmt.Errorf("请选择房间与 1–3 个候选座位")
	}
	seen := map[string]bool{}
	for _, seat := range seats {
		if seat == "" || seen[seat] {
			return storage.Job{}, fmt.Errorf("候选座位为空或重复")
		}
		seen[seat] = true
	}
	if days < 0 || days > 366 {
		return storage.Job{}, fmt.Errorf("持续天数须为 1–366")
	}
	start, err := time.ParseInLocation("2006-01-02", startDay, Zone)
	if err != nil || startDay < now.In(Zone).Format("2006-01-02") {
		return storage.Job{}, fmt.Errorf("开始日期须为今天或之后的有效日期")
	}
	if len(plan) != 7 {
		return storage.Job{}, fmt.Errorf("周计划必须包含周一至周日")
	}
	if mode != "daily" && mode != "weekdays" && mode != "weekly" {
		return storage.Job{}, fmt.Errorf("请选择自动规则")
	}
	names := []string{"周一", "周二", "周三", "周四", "周五", "周六", "周日"}
	enabled := false
	for i, p := range plan {
		if !p.Enabled {
			continue
		}
		enabled = true
		a, e1 := time.Parse("15:04", p.Start)
		b, e2 := time.Parse("15:04", p.End)
		if e1 != nil || e2 != nil || !b.After(a) {
			return storage.Job{}, fmt.Errorf("%s 已启用，请为它选择有效时间段", names[i])
		}
	}
	if !enabled {
		return storage.Job{}, fmt.Errorf("请至少启用一个星期几")
	}
	until := start.AddDate(0, 0, days-1)
	if days == 0 {
		until = start.AddDate(0, 0, 7)
	}
	j := storage.Job{ID: fmt.Sprintf("%d", now.UnixNano()), RoomID: roomID, Room: room, Seats: append([]string(nil), seats...), Created: now, RepeatStart: startDay, RepeatUntil: until.Format("2006-01-02"), RepeatMode: mode, WeekPlan: append([]storage.DayPlan(nil), plan...)}
	if days == 0 {
		j.RepeatForever = true
		j.RepeatUntil = ""
	}
	for day := start; !day.After(until); day = day.AddDate(0, 0, 1) {
		if !activeOn(j, day) {
			continue
		}
		end, _ := time.ParseInLocation("2006-01-02 15:04", day.Format("2006-01-02")+" "+endOn(j, day), Zone)
		if !end.After(now) {
			continue
		}
		setDailyDate(&j, day)
		return j, nil
	}
	return storage.Job{}, fmt.Errorf("持续日期范围内没有可预约的启用日期，请调整开始日期、天数或星期")
}

func Recurring(j storage.Job) bool { return j.RepeatForever || j.RepeatUntil != "" }
