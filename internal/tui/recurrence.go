package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"time"
	"wfuseat/internal/schedule"
	"wfuseat/internal/storage"
)

var autoModes = []string{"每天", "周一～周五", "按周循环"}
var autoWeekdays = []string{"周一", "周二", "周三", "周四", "周五", "周六", "周日"}

func (m *Model) autoPreview() bool { return m.days.cursor == dayRows }
func (m *Model) autoStartDay() string {
	if m.repeatStart != "" {
		return m.repeatStart
	}
	return time.Now().In(schedule.Zone).Format("2006-01-02")
}
func (m *Model) initAutoDraft() {
	if m.repeatInitialized {
		return
	}
	m.repeatInitialized = true
	if m.room.periodExplicit && len(m.room.endpoints) > 0 {
		if slot, ok := m.room.currentSlot(); ok {
			m.repeatCommon = storage.DayPlan{Enabled: true, Start: slot.StartTime, End: slot.EndTime}
		}
	}
	m.repeatWeek[0] = m.repeatCommon
	m.repeatWeek[0].Enabled = false
}
func (m *Model) storeAutoInterval() {
	var p storage.DayPlan
	if len(m.room.endpoints) > 0 {
		if s, ok := m.room.currentSlot(); ok {
			p.Start = s.StartTime
			p.End = s.EndTime
		}
	}
	if m.repeatMode == 2 {
		p.Enabled = p.Start != "" && p.End != ""
		m.repeatWeek[m.repeatWeekCursor] = p
	} else {
		p.Enabled = true
		m.repeatCommon = p
	}
}
func (m *Model) restoreAutoInterval() {
	p := m.repeatCommon
	if m.repeatMode == 2 {
		p = m.repeatWeek[m.repeatWeekCursor]
	}
	m.room.endpoints = nil
	m.room.periodExplicit = true
	lo, hi := -1, -1
	for i, s := range m.room.slots {
		if s.StartTime == p.Start {
			lo = i
		}
		if s.EndTime == p.End {
			hi = i
		}
	}
	if lo >= 0 && hi >= lo {
		m.room.endpoints = []int{lo}
		if hi != lo {
			m.room.endpoints = append(m.room.endpoints, hi)
		}
		m.room.slotIndex = lo
		if _, ok := m.room.currentSlot(); !ok {
			m.room.endpoints = nil
		}
	}
	m.selection = nil
	m.genSelect++
}
func (m *Model) cycleAutoFocus(back bool) {
	order := []int{1, 2, 0}
	if m.repeatMode == 2 {
		order = []int{1, 2, 4, 5, 0}
	}
	at := len(order) - 1
	for i, v := range order {
		if v == m.repeatFocus {
			at = i
			break
		}
	}
	step := 1
	if back {
		step = len(order) - 1
	}
	m.repeatFocus = order[(at+step)%len(order)]
}
func isAutoControlKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "j", "k", "h", "l", "up", "down", "left", "right", "enter", " ":
		return true
	}
	return msg.Type == tea.KeySpace
}
func (m *Model) keyRepeatOptions(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.initAutoDraft()
	delta := 0
	switch msg.String() {
	case "j", "down", "l", "right":
		delta = 1
	case "k", "up", "h", "left":
		delta = -1
	}
	if delta != 0 {
		switch m.repeatFocus {
		case 1:
			m.repeatModeCursor = (m.repeatModeCursor + delta + 3) % 3
		case 4:
			m.repeatWeekCursor = (m.repeatWeekCursor + delta + 7) % 7
			m.restoreAutoInterval()
		case 5:
			m.repeatCopy = (m.repeatCopy + delta + 2) % 2
		default:
			m.cycleAutoFocus(delta < 0)
		}
		return m, nil
	}
	switch m.repeatFocus {
	case 1:
		m.repeatMode = m.repeatModeCursor
		if m.repeatMode == 2 && !m.repeatWeekInitialized {
			m.repeatWeek[0] = m.repeatCommon
			m.repeatWeek[0].Enabled = false
			m.repeatWeekInitialized = true
		}
		m.restoreAutoInterval()
		if msg.Type == tea.KeyEnter {
			m.repeatFocus = 2
		}
	case 2:
		m.repeatInput = textInput()
		m.repeatInput.SetValue(m.autoStartDay())
		m.repeatInput.CharLimit = 10
		m.repeatEditing = true
		return m, m.repeatInput.Focus()
	case 4:
		if msg.Type == tea.KeyEnter {
			m.repeatFocus = 0
		} else {
			m.repeatWeek[m.repeatWeekCursor].Enabled = !m.repeatWeek[m.repeatWeekCursor].Enabled
		}
	case 5:
		m.copyAutoInterval()
	}
	return m, nil
}
func (m *Model) copyAutoInterval() {
	source := m.repeatWeek[m.repeatWeekCursor]
	if source.Start == "" || source.End == "" {
		m.pushToast(sevWarn, "先为当前星期选择时间段")
		return
	}
	for i := range m.repeatWeek {
		if (m.repeatCopy == 0 && i < 5) || (m.repeatCopy == 1 && m.repeatWeek[i].Enabled) {
			m.repeatWeek[i].Start = source.Start
			m.repeatWeek[i].End = source.End
			if m.repeatCopy == 0 {
				m.repeatWeek[i].Enabled = true
			}
		}
	}
	m.pushToast(sevOK, "已复制 %s 的区间；请查看周计划", autoWeekdays[m.repeatWeekCursor])
}
func (m *Model) editRepeatStart(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyEsc {
		m.repeatEditing = false
		m.repeatInput.Blur()
		return m, nil
	}
	if msg.Type == tea.KeyEnter {
		value := m.repeatInput.Value()
		_, err := time.ParseInLocation("2006-01-02", value, schedule.Zone)
		if err != nil || value < time.Now().In(schedule.Zone).Format("2006-01-02") {
			m.pushToast(sevWarn, "请输入今天或之后的日期：YYYY-MM-DD")
			return m, nil
		}
		m.repeatStart = value
		m.repeatEditing = false
		m.repeatInput.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.repeatInput, cmd = m.repeatInput.Update(msg)
	return m, cmd
}
func (m *Model) nextAutoWeekday() {
	m.repeatWeekCursor = (m.repeatWeekCursor + 1) % 7
	m.restoreAutoInterval()
	m.repeatFocus = 0
}
func (m *Model) autoRuleLine(width int, focused bool) string {
	var parts []string
	for i, label := range autoModes {
		mark := "○ "
		if i == m.repeatMode {
			mark = "● "
		}
		style := m.theme.RowNormal
		if focused && m.repeatFocus == 1 && i == m.repeatModeCursor {
			style = m.rowStyle(true, true)
		}
		parts = append(parts, style.Render(mark+label))
	}
	return m.clip(strings.Join(parts, "   "), width)
}
func (m *Model) repeatHeader(width int, focused bool) []string {
	title := "自动预订 · 已选中"
	if !m.repeatEnabled {
		title = "自动预订 · 预览（左侧空格选中）"
	}
	lines := []string{m.theme.Heading.Render(m.clip(title, width)), ""}
	labelWidth := 10
	field := func(label, value string, selected bool) string {
		return m.listRow(width, selected, focused, m.plainCell(label, labelWidth), m.plainCell(value, max(1, width-labelWidth-3)))
	}
	// Radio options own their individual focus style; an outer selected row
	// would paint every option and its padding as selected as well.
	ruleActive := focused && m.repeatFocus == 1
	cursor := " "
	if ruleActive {
		cursor = m.glyphs.Selected
	}
	lines = append(lines, m.cursorStyle(ruleActive).Render(m.fit(cursor, 1))+" "+m.column("重复规则", labelWidth)+m.autoRuleLine(max(1, width-labelWidth-3), focused))
	date := m.autoStartDay()
	if m.repeatEditing {
		date = m.repeatInput.View()
	}
	lines = append(lines, field("开始日期", date, m.repeatFocus == 2), field("结束条件", "手动暂停或取消", false))
	if m.repeatMode == 2 {
		actions := []string{"工作日并启用", "所有已启用日期"}
		var buttons []string
		for i, v := range actions {
			if focused && m.repeatFocus == 5 && i == m.repeatCopy {
				v = "[" + v + "]"
			}
			buttons = append(buttons, v)
		}
		lines = append(lines, field("复制区间", strings.Join(buttons, " / "), m.repeatFocus == 5))
	}
	lines = append(lines, "", m.theme.MutedText.Render(m.clip("参考日期 "+m.rooms.day+" · 执行时重新验证时段", width)), "")
	return lines
}
func (m *Model) automaticBody(width, height int, focused bool) []string {
	header := m.repeatHeader(width, focused)
	h := max(1, height-len(header))
	if m.repeatMode != 2 {
		m.periodWidth = width
		return padList(append(header, m.periodSelectionBody(width, h, focused && m.repeatFocus == 0)...), height)
	}
	if width >= 72 {
		leftWidth := 30
		rightWidth := width - leftWidth - 3
		m.periodWidth = rightWidth
		left := append([]string{m.theme.Heading.Render("  " + m.column("启用", 4) + m.column("星期", 6) + "预约区间")}, m.weekRows(leftWidth, max(0, h-1), focused)...)
		right := append([]string{m.theme.Heading.Render(autoWeekdays[m.repeatWeekCursor] + " · 空格选端点，Enter 下一天")}, m.periodSelectionBody(rightWidth, max(1, h-1), focused && m.repeatFocus == 0)...)
		for i := 0; i < h; i++ {
			header = append(header, m.fit(left[i], leftWidth)+" │ "+m.fit(right[i], rightWidth))
		}
	} else {
		rows := m.weekRows(width, min(7, max(1, h/2)), focused)
		header = append(header, rows...)
		m.periodWidth = width
		header = append(header, m.periodSelectionBody(width, max(1, height-len(header)), focused && m.repeatFocus == 0)...)
	}
	return padList(header, height)
}
func (m *Model) weekRows(width, height int, focused bool) []string {
	first := max(0, m.repeatWeekCursor-height+1)
	var lines []string
	for i := first; i < 7 && len(lines) < height; i++ {
		p := m.repeatWeek[i]
		interval := "未选时间"
		if p.Start != "" {
			interval = p.Start + "–" + p.End
		}
		lines = append(lines, m.listRow(width, i == m.repeatWeekCursor, focused && m.repeatFocus == 4, m.plainCell(func() string {
			if p.Enabled {
				return "[x]"
			}
			return "[ ]"
		}(), 4), m.plainCell(autoWeekdays[i], 6), m.plainCell(interval, max(1, width-12))))
	}
	return padList(lines, height)
}
func (m *Model) automaticPlan() []storage.DayPlan {
	if m.repeatMode == 2 {
		return append([]storage.DayPlan(nil), m.repeatWeek[:]...)
	}
	plan := make([]storage.DayPlan, 7)
	for i := range plan {
		plan[i] = m.repeatCommon
		plan[i].Enabled = m.repeatMode == 0 || i < 5
	}
	return plan
}

// Automatic planning has its own reference date; opening a date row is not a prerequisite.
func (m *Model) ensureAutomaticIntervals() tea.Cmd {
	if m.session.phase != phaseConfirmed {
		m.pushToast(sevWarn, "请先登录")
		return nil
	}
	if m.room.loadingRoom || m.rooms.loading {
		return nil
	}
	if len(m.room.slots) > 0 && m.room.err == nil {
		return nil
	}
	id := m.room.id
	if id == 0 {
		if room, ok := m.selectedRoom(); ok {
			id = room.ID
		}
	}
	if id == 0 {
		m.pushToast(sevWarn, "请先选择教室，再选择自动")
		return nil
	}
	reference := m.autoStartDay()
	today := time.Now().In(schedule.Zone)
	// Tomorrow provides complete intervals even after today's slots have ended.
	if reference <= today.Format("2006-01-02") || reference > today.AddDate(0, 0, 6).Format("2006-01-02") {
		reference = today.AddDate(0, 0, 1).Format("2006-01-02")
	}
	if m.rooms.day != reference {
		cmd := m.adoptDay(reference)
		m.days.autoOpen = id
		return cmd
	}
	return m.beginOpenRoom(id)
}
