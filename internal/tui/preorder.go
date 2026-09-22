package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"wfuseat/internal/chaoxing"
)

// 预订 (pre-order)
//
// Choosing a seat that is free right now is a *selection*: it can be signed and
// submitted straight away. Choosing one that cannot be reserved at this moment --
// it is taken, the room paused it, or the day's window has not opened yet -- is a
// **pre-order**: a recorded intent to take that seat when it becomes possible.
//
// Pre-orders are written into the project's own memory (work/preorders.json next
// to the request log), so they survive a restart and can be reviewed and
// cancelled from the 预订记录 panel. They are never sent: the tool fires
// simulations, and a real submission stays a manual, confirmed action.

// preorderRecord is one remembered intent.
type preorderRecord struct {
	ID      string `json:"id"`
	RoomID  int    `json:"room_id"`
	Room    string `json:"room"`
	Day     string `json:"day"`
	Start   string `json:"start"`
	End     string `json:"end"`
	SeatNum string `json:"seat_num"`
	// Note is why it had to be a pre-order, in the words of the moment.
	Note    string    `json:"note"`
	Created time.Time `json:"created"`
}

// preorderKey identifies the same seat on the same day and period, so booking it
// twice replaces the record instead of duplicating it.
func (p preorderRecord) key() string {
	return fmt.Sprintf("%d|%s|%s-%s|%s", p.RoomID, p.Day, p.Start, p.End, p.SeatNum)
}

// preorderPath is the project-level store, next to the request log.
func (m *Model) preorderPath() string {
	dir := m.cfg.LogDir
	if dir == "" {
		dir = "work/login_logs"
	}
	return filepath.Join(filepath.Dir(dir), "preorders.json")
}

// loadPreorders reads the store. A missing file is not an error: it is an empty
// memory.
func loadPreorders(path string) ([]preorderRecord, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("无法读取预订记录 %s: %w", path, err)
	}
	var items []preorderRecord
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("预订记录 %s 不是合法 JSON: %w", path, err)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Created.After(items[j].Created) })
	return items, nil
}

// savePreorders writes the store with owner-only permissions, through a temporary
// file so a crash cannot truncate it.
func savePreorders(path string, items []preorderRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("无法创建预订记录目录: %w", err)
	}
	raw, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("无法写入预订记录: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("无法保存预订记录: %w", err)
	}
	return nil
}

// --- model wiring -----------------------------------------------------------

// preordersLoadedMsg carries the store's contents.
type preordersLoadedMsg struct {
	items []preorderRecord
	err   error
}

// preordersSavedMsg reports the outcome of a write.
type preordersSavedMsg struct {
	items []preorderRecord
	err   error
}

// cmdLoadPreorders reads the store off the event loop.
func (m *Model) cmdLoadPreorders() tea.Cmd {
	if m.db != nil {
		return m.cmdJobs()
	}
	path := m.preorderPath()
	return func() tea.Msg {
		items, err := loadPreorders(path)
		return preordersLoadedMsg{items: items, err: err}
	}
}

// cmdSavePreorders writes the store off the event loop.
func (m *Model) cmdSavePreorders(items []preorderRecord) tea.Cmd {
	path := m.preorderPath()
	return func() tea.Msg {
		return preordersSavedMsg{items: items, err: savePreorders(path, items)}
	}
}

func (m *Model) onPreordersLoaded(msg preordersLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.preorders.err = classify(msg.err)
		return m, nil
	}
	m.preorders.err = nil
	m.preorders.items = msg.items
	m.preorders.list.SetCount(len(m.preorders.items))
	m.layout()
	return m, nil
}

func (m *Model) onPreordersSaved(msg preordersSavedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.preorders.err = &AppError{Kind: kindInput, Short: "预订记录写入失败", Detail: msg.err.Error()}
		m.pushToast(sevErr, "预订记录写入失败：%s", msg.err)
		return m, nil
	}
	m.preorders.err = nil
	m.preorders.path = m.preorderPath()
	m.layout()
	return m, nil
}

// addPreorder records an intent, replacing any earlier record for the same seat,
// day and period so the memory cannot grow duplicates by pressing space twice.
func (m *Model) addPreorder(record preorderRecord) tea.Cmd {
	record.ID = record.key()
	record.Created = time.Now()
	items := make([]preorderRecord, 0, len(m.preorders.items)+1)
	for _, existing := range m.preorders.items {
		if existing.key() != record.key() {
			items = append(items, existing)
		}
	}
	items = append([]preorderRecord{record}, items...)
	m.preorders.items = items
	m.preorders.list.SetCount(len(items))
	m.preorders.list.cursor = 0
	m.layout()
	return m.cmdSavePreorders(items)
}

// removePreorder forgets one record.
func (m *Model) removePreorder(id string) tea.Cmd {
	items := make([]preorderRecord, 0, len(m.preorders.items))
	for _, existing := range m.preorders.items {
		if existing.ID != id {
			items = append(items, existing)
		}
	}
	m.preorders.items = items
	m.preorders.list.SetCount(len(items))
	m.preorders.list.clamp()
	m.layout()
	return m.cmdSavePreorders(items)
}

// signReason explains, in short form, why nothing can be signed right now.
func (m *Model) signReason() string {
	if note := m.client.SignNote(); note != "" {
		return note
	}
	return "预约窗口未开放"
}

// reservableNowFree is reservableNow without the reason, for the places that only
// need the answer.
func (m *Model) reservableNowFree(number string) bool {
	free, _ := m.reservableNow(number)
	return free
}

// reservableNow reports whether a seat can be signed and submitted at this
// moment. Anything else is a pre-order: a taken seat may be free later, and a day
// whose window has not opened has no submit token at all.
func (m *Model) reservableNow(number string) (bool, string) {
	if !m.client.CanSign() {
		return false, m.signReason()
	}
	state, known := m.seatState(number)
	if !known {
		return false, "该时段尚未查询"
	}
	if state.Status != chaoxing.SeatFree {
		return false, "当前" + state.Status.String()
	}
	return true, ""
}

// preorderSelectedSeat is the seat panel's "space" when the seat cannot be taken
// now: remember it instead of signing it.
func (m *Model) preorderSelectedSeat() (tea.Model, tea.Cmd) {
	if m.room.id == 0 {
		m.pushToast(sevInfo, "先在 %s 选中一间自习室", paneTitle(PaneRooms))
		return m, nil
	}
	number, ok := m.selectedSeatNumber()
	if !ok {
		m.pushToast(sevInfo, "没有可预订的座位")
		return m, nil
	}
	// A pre-order does not need a period. When the day has none -- its window has
	// not opened, or every period is gone -- the intent is still worth remembering,
	// so it is recorded without one and said so.
	slot, hasSlot := m.room.currentSlot()
	_, reason := m.reservableNow(number)
	if !hasSlot && reason == "" {
		reason = "该日时段未开放"
	}
	record := preorderRecord{
		RoomID:  m.room.id,
		Room:    m.room.name,
		Day:     m.dayOrDash(),
		SeatNum: number,
		Note:    reason,
	}
	if hasSlot {
		record.Start, record.End = slot.StartTime, slot.EndTime
	}
	cmd := m.addPreorder(record)
	// The keyboard stays on the seat: the toast names the record, the 预订记录 panel
	// shows its new count, and the chosen seat is marked where the user is looking.
	m.pushToast(sevOK, "已预订 %s 号 · %s · 记入 %s", number, reason, m.preorderPath())
	return m, cmd
}

// preorderPeriodText renders a record's period, which a day without one leaves
// open. It never prints an empty range.
func preorderPeriodText(item preorderRecord) string {
	if item.Start == "" && item.End == "" {
		return "全时段（当日未开放）"
	}
	return item.Start + "–" + item.End
}

// preordersBodyLines is the 预订记录 panel: one row per remembered intent.
func (m *Model) preordersBodyLines(width, height int, focused bool) []string {
	if m.db != nil {
		return m.jobRows(width, height, focused)
	}
	switch {
	case m.preorders.err != nil && len(m.preorders.items) == 0:
		return toLines(m.errorView(m.preorders.err, width, height), height)
	case len(m.preorders.items) == 0:
		return toLines(m.readOnlyView("还没有预订",
			"座位当前不可预约时（已占用/暂停/窗口未开放），按 space 会把它记在这里。",
			width, height), height)
	}

	const (
		seatWidth = 5
		dateWidth = 11
	)
	showDate := width >= seatWidth+dateWidth+8
	first, last := m.preorders.list.Window()
	lines := make([]string, 0, height)
	for i := first; i < last; i++ {
		item := m.preorders.items[i]
		selected := i == m.preorders.list.cursor
		cells := []listCell{m.plainCell(item.SeatNum, seatWidth)}
		if showDate {
			cells = append(cells, m.mutedCell(m.clip(item.Day, dateWidth), dateWidth))
		}
		// The state is computed now, not stored: a seat that was taken when it was
		// pre-ordered may be free already.
		status, style := "待窗口", m.theme.BadgeMuted
		switch state, known := m.seatState(item.SeatNum); {
		case m.reservableNowFree(item.SeatNum):
			status, style = "可提交", m.theme.BadgeOK
		case known && state.Status == chaoxing.SeatOccupied:
			status, style = "已被占用", m.theme.BadgeErr
		}
		cells = append(cells, listCell{
			text:  m.column(status, max(width-seatWidth-dateWidth-2, 8)),
			style: style,
		})
		lines = append(lines, m.listRow(width, selected, focused, cells...))
	}
	return padList(lines, height)
}

func (m *Model) preordersTrailing() string {
	if m.db != nil {
		return itoa(len(m.jobs)) + " 条"
	}
	if len(m.preorders.items) == 0 {
		return ""
	}
	return itoa(len(m.preorders.items)) + " 条"
}

func (m *Model) preordersStatusLine() string {
	if m.db != nil {
		return "账号 " + m.accountLabel() + " · space 取消 · r 刷新"
	}
	switch {
	case m.preorders.err != nil:
		return "记录读写失败：" + m.preorders.err.Short
	case len(m.preorders.items) == 0:
		return "只读 · 没有预订"
	}
	item, ok := m.selectedPreorder()
	if !ok {
		return "未选中预订"
	}
	return item.SeatNum + " 号 · space 取消预订 · enter 看详情"
}

// selectedPreorder is the record under the cursor.
func (m *Model) selectedPreorder() (preorderRecord, bool) {
	index, ok := m.preorders.list.Selected()
	if !ok || index >= len(m.preorders.items) {
		return preorderRecord{}, false
	}
	return m.preorders.items[index], true
}

// keyRecords handles the 预订记录 panel: movement, cancel, detail.
func (m *Model) keyRecords(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Up):
		m.preorders.list.Move(-1)
	case key.Matches(msg, m.keys.Down):
		m.preorders.list.Move(1)
	case key.Matches(msg, m.keys.Top):
		m.preorders.list.GotoTop()
	case key.Matches(msg, m.keys.End):
		m.preorders.list.GotoBottom()
	case key.Matches(msg, m.keys.PageUp):
		m.preorders.list.Page(-1)
	case key.Matches(msg, m.keys.PageDown):
		m.preorders.list.Page(1)
	case key.Matches(msg, m.keys.Refresh):
		return m, m.cmdLoadPreorders()
	case key.Matches(msg, m.keys.Space):
		return m.beginCancelPreorder()
	case key.Matches(msg, m.keys.Enter):
		// enter steps into the record's own page; the detail is on the right.
		m.focusZone(ZoneMain)
	}
	return m, nil
}

// beginCancelPreorder asks before forgetting a record.
func (m *Model) beginCancelPreorder() (tea.Model, tea.Cmd) {
	if m.db != nil {
		j, ok := m.selectedJob()
		if !ok {
			return m, nil
		}
		m.pendingCancelJobID = j.ID
		m.confirm = confirmState{prompt: "取消此自动预订？", detail: strings.Join(j.Seats, " → ") + " · " + j.Day, confirm: "取消任务", action: confirmCancelJob}
		m.overlay = OverlayConfirm
		return m, nil
	}
	item, ok := m.selectedPreorder()
	if !ok {
		m.pushToast(sevInfo, "没有选中的预订")
		return m, nil
	}
	m.confirm = confirmState{
		prompt:   fmt.Sprintf("取消 %s 号 %s %s 的预订？", item.SeatNum, item.Day, preorderPeriodText(item)),
		detail:   "只删除项目里的预订记录（" + m.preorderPath() + "），不涉及任何网络请求。",
		confirm:  "取消预订",
		action:   confirmCancelPreorder,
		focusYes: false,
	}
	m.overlay = OverlayConfirm
	return m, nil
}

// preordersDetailBody is the content beside the panel: everything known about the
// record under the cursor.
func (m *Model) preordersDetailBody(width, height int, focused bool) []string {
	if m.db != nil {
		return m.jobDetails(width, height)
	}
	item, ok := m.selectedPreorder()
	if !ok {
		return toLines(m.readOnlyView("只读 · 未选择预订",
			"在 "+paneTitle(PaneRecords)+" 用 j/k 选择一条预订。", width, height), height)
	}
	status := "待窗口"
	if m.reservableNowFree(item.SeatNum) {
		status = "可提交（该座位现在空闲）"
	}
	rows := [][2]string{
		{"座位", item.SeatNum},
		{"自习室", item.Room},
		{"日期", item.Day},
		{"时段", preorderPeriodText(item)},
		{"预订原因", item.Note},
		{"当前状态", status},
		{"记录时间", item.Created.Format("2006-01-02 15:04:05")},
		{"存储位置", m.preorderPath()},
	}
	lines := m.detailRows(width, height, rows)
	hint := "space 取消这条预订（只删记录，不发请求）。该座位现在可预约时，到 " +
		paneTitle(PaneSeats) + " 按 space 直接选中并提交。"
	for _, wrapped := range wrapText(hint, width) {
		if len(lines) >= height {
			break
		}
		lines = append(lines, m.theme.FaintText.Render(wrapped))
	}
	return padList(lines, height)
}

func (m *Model) preordersDetailStatus() string {
	if m.db != nil {
		return "自动预订 · space 取消尚未执行的任务"
	}
	if _, ok := m.selectedPreorder(); !ok {
		return "只读 · 未选择预订"
	}
	return "space 取消预订 · 记录只保存在项目里"
}

func (m *Model) preordersDetailTrail() string {
	item, ok := m.selectedPreorder()
	if !ok {
		return ""
	}
	return item.SeatNum
}

// preorderSummary is the header's short description of the store.
func (m *Model) preorderSummary() string {
	if len(m.preorders.items) == 0 {
		return ""
	}
	return fmt.Sprintf("%d 条预订", len(m.preorders.items))
}
