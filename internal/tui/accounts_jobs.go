package tui

import (
	"encoding/json"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"time"
	"wfuseat/internal/config"
	"wfuseat/internal/schedule"
	"wfuseat/internal/storage"
)

type jobsMsg struct {
	account string
	items   []storage.Job
	err     error
}
type jobSavedMsg struct {
	err     error
	account string
}

func (m *Model) initAccount() error {
	if m.remote != nil {
		m.db = m.remote
		m.restoreSession = m.remote.Authorized()
		m.settings.draft = m.cfg
		return nil
	}
	if m.cfg.Account == "" {
		return nil
	}
	dir, err := storage.AccountDir(m.cfg.Root, m.cfg.Account)
	if err != nil {
		return err
	}
	m.db, err = storage.Open(dir)
	if err != nil {
		return err
	}
	raw, err := m.db.Get("cookies")
	if err != nil {
		return err
	}
	db := m.db
	if err = m.login.Session.AttachCookies(raw, func(b []byte) error { return db.Put("cookies", b) }); err != nil {
		return err
	}
	m.restoreSession = len(raw) > 0
	m.settings.draft = m.cfg
	return nil
}
func (m *Model) cmdJobs() tea.Cmd {
	db, account := m.db, m.cfg.Account
	return func() tea.Msg { items, err := db.Jobs(); return jobsMsg{items: items, err: err, account: account} }
}
func (m *Model) switchAccount(id string) (tea.Model, tea.Cmd) {
	cfg, path, err := config.LoadAccount(m.cfg.Root, id)
	if err != nil {
		m.pushToast(sevErr, "%s", err)
		return m, nil
	}
	if _, err = config.Save(cfg); err != nil {
		m.pushToast(sevErr, "账号配置保存失败")
		return m, nil
	}
	var opts []Option
	if m.remote != nil {
		client := m.remote.Fresh()
		if storage.IsServiceAccount(id) {
			if e := client.LoadCredentials(id); e != nil {
				m.pushToast(sevErr, "远程会话不可用：%s", e)
				return m, nil
			}
		}
		opts = append(opts, WithRemote(client))
	}
	next, err := New(cfg, path, opts...)
	if err != nil {
		m.pushToast(sevErr, "账号打开失败：%s", err)
		return m, nil
	}
	next.genSession = m.genSession + 1000
	next.genRooms = m.genRooms + 1000
	next.genRoom = m.genRoom + 1000
	next.genSeats = m.genSeats + 1000
	next.genLogs = m.genLogs + 1000
	next.genScan = m.genScan + 1000
	next.genWrite = m.genWrite + 1000
	next.genSelect = m.genSelect + 1000
	next.width, next.height, next.ready = m.width, m.height, m.ready
	next.zone = ZoneMain
	next.layout()
	m.Close()
	return next, next.Init()
}
func (m *Model) busyClient() bool {
	return m.session.busy || m.room.loadingRoom || m.room.loadingSeats || m.rooms.loading || m.room.scanning || m.write.busy
}
func (m *Model) toggleCandidate(number string) (tea.Model, tea.Cmd) {
	for i, s := range m.candidates {
		if s == number {
			m.candidates = append(m.candidates[:i], m.candidates[i+1:]...)
			m.selection = nil
			m.genSelect++
			m.pushToast(sevInfo, "已移除 %s；顺序 %s", number, strings.Join(m.candidates, " → "))
			m.layout()
			return m, nil
		}
	}
	if len(m.candidates) >= 3 {
		m.pushToast(sevWarn, "最多三个候选座位；空格取消已有选择")
		return m, nil
	}
	m.candidates = append(m.candidates, number)
	m.selection = nil
	m.genSelect++
	m.pushToast(sevOK, "候选顺序 %s", strings.Join(m.candidates, " → "))
	m.layout()
	return m, nil
}
func (m *Model) beginScheduledJob() (tea.Model, tea.Cmd) {
	if !m.cfg.AllowSubmit {
		m.pushToast(sevWarn, "此账号已暂停预订，在账号面板按 p 开启")
		return m, nil
	}
	if m.busyClient() {
		m.pushToast(sevWarn, "等待当前查询完成后再创建任务")
		return m, nil
	}
	var job storage.Job
	var err error
	if m.repeatEnabled {
		m.initAutoDraft()
		modes := []string{"daily", "weekdays", "weekly"}
		job, err = schedule.NewAutomaticJob(m.room.id, m.room.name, m.candidates, m.autoStartDay(), 0, modes[m.repeatMode], m.automaticPlan(), time.Now())
		if err != nil {
			m.pushToast(sevWarn, "%s", err)
			return m, nil
		}
	} else {
		slot, ok := m.room.currentSlot()
		if !ok {
			m.pushToast(sevWarn, "需要先选择预约时段；不能自动发送空时段")
			return m, nil
		}
		status, opens, known := m.client.ReserveWindow()
		if !known || (status != "AVAILABLE" && status != "BEFORE_OPEN") || (status == "BEFORE_OPEN" && opens.IsZero()) {
			m.pushToast(sevWarn, "请重新打开房间获取开放时间")
			return m, nil
		}
		if status == "AVAILABLE" || opens.IsZero() {
			opens = time.Now().Add(2 * time.Second)
		}
		clock := opens.In(schedule.Zone).Format("15:04:05")

		job, err = schedule.NewJob(m.room.id, m.room.name, m.rooms.day, slot.StartTime, slot.EndTime, m.candidates, "", clock, time.Now())
		if err != nil {
			m.pushToast(sevWarn, "%s", err)
			return m, nil
		}
		if job.Cron == "" {
			job.Next = opens
			job.RoomWindow = true
			job.GlobalTime = ""
		}

	}
	m.pendingJob = &job
	mode := "一次性"
	if schedule.Recurring(job) {
		mode = dailyDescription(job) + weeklyDescription(job)
	}
	if job.Cron != "" {
		mode = "循环 cron：" + job.Cron + fmt.Sprintf("；预约日为执行日 %+d 天", job.DayOffset)
	}
	m.confirm = confirmState{prompt: "创建自动预订？", detail: fmt.Sprintf("到时会发送真实请求。固定延迟 %d ms（执行时读取账号配置）；明确拒绝才换座，结果未知暂停。", m.cfg.DelayMS), request: fmt.Sprintf("账号 %s\n日期 %s · %s–%s\n座位 %s\n执行 %s（北京时间）\n%s", m.accountLabel(), job.Day, job.Start, job.End, strings.Join(job.Seats, " → "), jobNextLabel(job), mode), confirm: "加入预订", action: confirmSchedule}
	m.overlay = OverlayConfirm
	return m, nil
}
func (m *Model) saveScheduledJob() tea.Cmd {
	if m.pendingJob == nil {
		return nil
	}
	job := *m.pendingJob
	m.pendingJob = nil
	db, account := m.db, m.cfg.Account
	return func() tea.Msg { return jobSavedMsg{err: db.Add(job), account: account} }
}
func jobState(s string) string {
	switch s {
	case "completed":
		return "循环结束"
	case "pending":
		return "待执行"
	case "running":
		return "执行中"
	case "succeeded":
		return "成功"
	case "failed":
		return "失败"
	case "unknown":
		return "结果未知·暂停"
	case "needs_login":
		return "需重新登录"
	case "paused":
		return "已暂停"
	case "cancelled":
		return "已取消"
	case "missed":
		return "已错过"
	}
	return s
}
func (m *Model) jobRows(width, height int, focused bool) []string {
	if len(m.jobs) == 0 {
		return toLines(m.readOnlyView("没有自动预订", "座位空格多选（最多三座），a 创建。", width, height), height)
	}
	m.preorders.list.SetCount(len(m.jobs))
	first, last := m.preorders.list.Window()
	var lines []string
	for i := first; i < last; i++ {
		j := m.jobs[i]
		label := strings.Join(j.Seats, "→") + " " + jobState(j.State)
		lines = append(lines, m.listRow(width, i == m.preorders.list.cursor, focused, m.plainCell(label, max(width-2, 1))))
	}
	return padList(lines, height)
}
func (m *Model) selectedJob() (storage.Job, bool) {
	i, ok := m.preorders.list.Selected()
	if !ok || i >= len(m.jobs) {
		return storage.Job{}, false
	}
	return m.jobs[i], true
}
func (m *Model) jobDetails(width, height int) []string {
	j, ok := m.selectedJob()
	if !ok {
		return toLines(m.readOnlyView("未选择自动预订", "只显示当前账号的任务。", width, height), height)
	}
	repeat := "一次性"
	if schedule.Recurring(j) {
		repeat = dailyDescription(j)
	}
	if j.Cron != "" {
		repeat = j.Cron + fmt.Sprintf(" · 执行日 %+d 天", j.DayOffset)
	}
	delay := "无记录"
	if b, e := m.db.Get("last_delay/" + j.ID); e == nil && len(b) > 0 {
		var d struct {
			Delay int `json:"delay_ms"`
		}
		if json.Unmarshal(b, &d) == nil {
			delay = fmt.Sprintf("%d ms", d.Delay)
		}
	}
	rows := [][2]string{{"上次固定延迟", delay}, {"账号", m.accountLabel()}, {"房间", j.Room}, {"座位顺序", strings.Join(j.Seats, " → ")}, {"预约日", j.Day}, {"时段", j.Start + "–" + j.End}, {"计划", repeat}, {"下次执行", jobNextLabel(j)}, {"状态", jobState(j.State)}, {"上次结果", j.Result}, {"操作", "space 取消 · r 刷新；暂停项核实后新建"}}
	if len(j.WeekPlan) == 7 {
		for i, p := range j.WeekPlan {
			v := "停用"
			if p.Enabled {
				v = p.Start + "–" + p.End
			}
			rows = append(rows, [2]string{autoWeekdays[i], v})
		}
	}
	return m.detailRows(width, height, rows)
}
func (m *Model) cancelJob() tea.Cmd {
	id := m.pendingCancelJobID
	m.pendingCancelJobID = ""
	if id == "" {
		j, ok := m.selectedJob()
		if !ok {
			return nil
		}
		id = j.ID
	}
	db, account := m.db, m.cfg.Account
	return func() tea.Msg { return jobSavedMsg{err: db.Cancel(id), account: account} }
}

func dailyDescription(j storage.Job) string {
	weekends := "含周末"
	if j.SkipWeekends {
		weekends = "跳过周末"
	}
	mode := "每日循环"
	switch j.RepeatMode {
	case "daily":
		mode = "每天"
	case "weekdays":
		mode = "周一～周五"
	case "weekly":
		mode = "按周循环"
	}
	if len(j.WeekPlan) == 7 {
		weekends = "每日使用对应区间"
	}
	if j.RepeatForever {
		return fmt.Sprintf("%s · %s 起 · 持续至手动暂停或取消", mode, j.RepeatStart)
	}
	return fmt.Sprintf("%s · %s 至 %s（含）· %s", mode, j.RepeatStart, j.RepeatUntil, weekends)
}
func jobNextLabel(j storage.Job) string {
	if j.AwaitWindow {
		return "等待获取 " + j.Day + " 的教室开放时间"
	}
	return j.Next.In(schedule.Zone).Format("2006-01-02 15:04:05")
}

func weeklyDescription(j storage.Job) string {
	if len(j.WeekPlan) != 7 {
		return ""
	}
	var lines []string
	for i, p := range j.WeekPlan {
		value := "停用"
		if p.Enabled {
			value = p.Start + "–" + p.End
		}
		lines = append(lines, autoWeekdays[i]+" "+value)
	}
	return "\n" + strings.Join(lines, "\n")
}
