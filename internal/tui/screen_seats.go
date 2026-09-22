package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"wfuseat/internal/chaoxing"
)

// --- period panel ----------------------------------------------------------

func (m *Model) periodsStatusLine() string {
	if m.repeatEnabled {
		return fmt.Sprintf("自动 · %s · 持续至取消", autoModes[m.repeatMode])
	}
	switch {
	case m.room.err != nil:
		return m.roomErrorLabel() + " · d 诊断"
	case m.room.unsupported != nil:
		return m.room.unsupported.Short
	case m.room.scanning:
		return fmt.Sprintf("%s 扫描 %d/%d · esc 停止",
			m.spinner.View(), m.room.scanDone, len(m.room.slots))
	default:
		loaded := "未载入"
		if m.selectedDay() == m.rooms.day && len(m.room.slots) > 0 {
			loaded = itoa(len(m.room.slots)) + " 个时段"
		} else if m.selectedDay() == m.rooms.day && m.room.id != 0 {
			loaded = "0 个时段"
		}
		return m.dayOrDash() + " · " + loaded
	}
}

// periodsBodyLines is the day planner: seven days, then the repeat option that
// decides when a scheduled request fires.
func (m *Model) periodsBodyLines(width, height int, focused bool) []string {
	switch {
	case m.room.loadingRoom:
		return toLines(m.loadingView("正在打开房间…", width, height), height)
	}
	loaded := m.rooms.day
	if loaded == "" {
		loaded = m.client.Day
	}
	// The planner is seven rows tall and the panel may show fewer, so it walks
	// with the cursor the same way every other list does.
	m.room.periods.SetCount(periodRows)
	m.room.periods.cursor = plannerPosition(m.days.cursor)
	m.room.periods.clamp()
	first, last := m.room.periods.Window()
	lines := make([]string, 0, last-first)
	for position := first; position < last; position++ {
		row := plannerRow(position)
		date, label := m.dayAt(row)
		if row == dayRows {
			date = ""
			label = "自动"
		}
		selected := row == m.days.cursor
		style := m.theme.RowNormal
		if selected {
			style = m.rowStyle(true, focused)
		}
		lines = append(lines, m.listRow(width, selected, focused,
			m.committedMark((row == dayRows && m.repeatEnabled) || (!m.repeatEnabled && date != "" && date == loaded)),
			listCell{text: m.column(label, max(width-4, 8)), style: style},
		))
	}
	return padList(lines, height)
}

// --- seat panel ------------------------------------------------------------

// staticSeatCount is how many seats the room has, which is known as soon as the
// room is open and does not depend on any query. When the service sent its own seat
// listing, that listing is the count; otherwise the room's numbering range is.
func (m *Model) staticSeatCount() int {
	if len(m.room.seatOrder) > 0 {
		return len(m.room.seatOrder)
	}
	if !m.room.hasLayout {
		return 0
	}
	return m.room.layout.Total
}

// seatNumberAt is the nth seat of the open room, in the room's own numbering (or in
// the service's own listing order when it sent one). The listing is a copy the
// model keeps, so the list does not depend on the client still being on the room.
func (m *Model) seatNumberAt(index int) string {
	if index < 0 {
		return ""
	}
	if len(m.room.seatOrder) > 0 {
		if index >= len(m.room.seatOrder) {
			return ""
		}
		return m.room.seatOrder[index]
	}
	if !m.room.hasLayout || index >= m.room.layout.Total {
		return ""
	}
	return fmt.Sprintf("%03d", m.room.layout.Start+index)
}

// seatSpot reports where a seat is. The room's grid and zone labels come from the
// service; the ordinal is always available from the room's own numbering.
func (m *Model) seatSpot(number string) (chaoxing.SeatSpot, bool) {
	if !m.room.hasLayout {
		return chaoxing.SeatSpot{}, false
	}
	if spot, ok := m.client.SeatSpot(number); ok {
		return spot, true
	}
	if len(m.room.seatOrder) > 0 {
		// The service listed these seats, so its numbering range says nothing about
		// where one of them sits. Say nothing rather than something wrong.
		return chaoxing.SeatSpot{}, false
	}
	value, err := strconv.Atoi(number)
	if err != nil {
		return chaoxing.SeatSpot{}, false
	}
	ordinal := value - m.room.layout.Start + 1
	if ordinal < 1 || ordinal > m.room.layout.Total {
		return chaoxing.SeatSpot{}, false
	}
	return chaoxing.SeatSpot{
		Number:    number,
		Ordinal:   ordinal,
		GridTotal: m.room.layout.Grid,
	}, true
}

// visibleSeatNumbers is the static seat list after the filter.
func (m *Model) visibleSeatNumbers() []string {
	numbers := make([]string, 0, m.staticSeatCount())
	for i := 0; i < m.staticSeatCount(); i++ {
		if number := m.seatNumberAt(i); number != "" {
			numbers = append(numbers, number)
		}
	}
	return filterSeatNumbers(numbers, m.room.filter)
}

// selectedSeatNumber is the seat the static list is on.
func (m *Model) selectedSeatNumber() (string, bool) {
	numbers := m.visibleSeatNumbers()
	index, ok := m.room.seats.Selected()
	if !ok || index >= len(numbers) {
		return "", false
	}
	return numbers[index], true
}

func (m *Model) seatsTrailing() string {
	if order := m.room.seatOrder; len(order) > 0 {
		return order[0] + "–" + order[len(order)-1]
	}
	if !m.room.hasLayout {
		return ""
	}
	layout := m.room.layout
	return fmt.Sprintf("%03d–%03d", layout.Start, layout.Start+layout.Total-1)
}

// roomErrorLabel names the step that failed, so a failed query is not reported as
// a failed open.
func (m *Model) roomErrorLabel() string {
	if m.room.errLabel == "" {
		return "出错"
	}
	return m.room.errLabel
}

func (m *Model) seatsStatusLine() string {
	if m.room.loadingRoom {
		return m.spinner.View() + " 正在打开房间…"
	}
	if m.room.id == 0 {
		return "只读 · 未选择自习室"
	}
	// The room's numbering is what the list shows, and a day that failed to open
	// does not take it away: the failure is reported here, beside the seats it
	// could not affect, instead of replacing them.
	if m.room.err != nil {
		return fmt.Sprintf("列出 %d 座 · %s：%s · d 诊断", m.staticSeatCount(), m.roomErrorLabel(), m.room.err.Short)
	}
	if m.room.unsupported != nil {
		return fmt.Sprintf("列出 %d 座 · %s · d 诊断", m.staticSeatCount(), m.room.unsupported.Short)
	}
	if !m.client.CanSign() {
		// The room is open and its seats are all listed; only signing is missing,
		// and saying so is what turns "no seats" into "pre-order instead".
		return "只读 · 可预订 · " + m.signReason()
	}
	if m.filter.active {
		return "/ " + m.filter.input.Value()
	}
	if m.room.filter != "" {
		return "过滤：" + m.room.filter + " · esc 清除"
	}
	if seat, ok := m.selectedSeatNumber(); ok {
		return seat + " 号 · enter 打开座位图"
	}
	return "共 " + itoa(m.staticSeatCount()) + " 座"
}

// seatsBodyLines is the room's seat numbering. It is deliberately static: what a
// seat is doing right now is a separate question, answered in the content panel
// after enter, so the list never implies data it has not fetched.
func (m *Model) seatsBodyLines(width, height int, focused bool) []string {
	// A failure only replaces the list when there is no numbering to list: a room
	// that has already told us its seats keeps showing them for every day.
	switch {
	case m.room.loadingRoom:
		return toLines(m.loadingView("正在打开房间…", width, height), height)
	case m.room.unsupported != nil && m.staticSeatCount() == 0:
		return toLines(m.stateView(m.glyphs.Ring, sevWarn,
			m.room.unsupported.Short, m.room.unsupported.Detail, m.room.unsupported.Hint,
			width, height), height)
	case m.room.err != nil && m.staticSeatCount() == 0:
		return toLines(m.errorView(m.room.err, width, height), height)
	case m.room.id == 0:
		return toLines(m.readOnlyView("只读 · 未选择自习室",
			"在 "+paneTitle(PaneRooms)+" 按 enter 打开一间自习室。", width, height), height)
	case m.staticSeatCount() == 0:
		return toLines(m.readOnlyView("没有座位编号",
			"该房间未提供座位编号信息。", width, height), height)
	}

	numbers := m.visibleSeatNumbers()
	if len(numbers) == 0 {
		return toLines(m.emptyView("没有匹配的座位",
			"按 / 修改过滤条件，或按 esc 清除。", width, height), height)
	}
	const (
		markWidth   = 2
		numberWidth = 7
		gridWidth   = 12
	)
	showGrid := m.room.hasLayout && m.room.layout.Grid > 0 &&
		width >= markWidth+numberWidth+gridWidth+2
	// The dot marks every seat a choice is in force for -- signed or pre-ordered --
	// not merely the one the cursor is on.
	committed := m.chosenSeats()
	first, last := m.room.seats.Window()
	lines := make([]string, 0, height)
	for i := first; i < last; i++ {
		number := numbers[i]
		selected := i == m.room.seats.cursor
		cells := []listCell{m.committedMark(committed[number] != seatUncommitted), m.plainCell(number, numberWidth)}
		if showGrid {
			note := ""
			if spot, ok := m.seatSpot(number); ok && spot.GridOrdinal > 0 {
				note = fmt.Sprintf("网格 %d/%d", spot.GridOrdinal, spot.GridTotal)
			}
			cells = append(cells, m.mutedCell(note, max(width-markWidth-numberWidth-2, 8)))
		}
		lines = append(lines, m.listRow(width, selected, focused, cells...))
	}
	return padList(lines, height)
}

// seatPeriodNotes reports how a seat stands in the other periods that have been
// queried. Only cached periods appear, so nothing is inferred from a guess.
func (m *Model) seatPeriodNotes(seatNum string) string {
	current, ok := m.room.currentSlot()
	if !ok {
		return ""
	}
	notes := []string{}
	for _, slot := range m.room.slots {
		if slot.StartTime == current.StartTime && slot.EndTime == current.EndTime {
			continue
		}
		occupancy, cached := m.room.occupancyFor(slot.StartTime, slot.EndTime)
		if !cached {
			continue
		}
		status := chaoxing.SeatUnavailable
		for _, state := range occupancy.Seats {
			if state.Num == seatNum {
				status = state.Status
				break
			}
		}
		notes = append(notes, fmt.Sprintf("%s %s", slot.StartTime, m.seatStatusGlyph(status)))
		if len(notes) >= 4 {
			break
		}
	}
	return strings.Join(notes, "  ")
}

// seatStatusGlyph is the light marker for one seat state.
func (m *Model) seatStatusGlyph(status chaoxing.SeatStatus) string {
	switch status {
	case chaoxing.SeatFree:
		return m.glyphs.Dot
	case chaoxing.SeatOccupied:
		return m.glyphs.Cross
	}
	return m.glyphs.Bullet
}

// seatStatusStyle colours one seat state. Anything unknown is muted rather than
// coloured as free.
func (m *Model) seatStatusStyle(status chaoxing.SeatStatus) lipgloss.Style {
	switch status {
	case chaoxing.SeatFree:
		return m.theme.BadgeOK
	case chaoxing.SeatOccupied:
		return m.theme.BadgeErr
	}
	return m.theme.BadgeMuted
}

// seatState looks a seat up in the period that is in use, if it has been queried.
func (m *Model) seatState(number string) (chaoxing.SeatState, bool) {
	occupancy, ok := m.room.currentOccupancy()
	if !ok {
		return chaoxing.SeatState{}, false
	}
	for _, state := range occupancy.Seats {
		if state.Num == number {
			return state, true
		}
	}
	return chaoxing.SeatState{}, false
}

// visibleSeatStates is the queried seat picture after the filter. It is what the
// content panel reads; the list itself is static.
func (m *Model) visibleSeatStates() []chaoxing.SeatState {
	occupancy, ok := m.room.currentOccupancy()
	if !ok {
		return nil
	}
	return filterSeatStates(occupancy.Seats, m.room.filter)
}

// --- keys ------------------------------------------------------------------

// keyPeriods handles the day planner. j/k move between the eight rows, enter
// loads the day the cursor is on (or opens the repeat options on its row), and s
// scans the loaded day so the grid beside it has numbers to show.
func (m *Model) keyPeriods(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.days.cursor == dayRows && (key.Matches(msg, m.keys.Space) || key.Matches(msg, m.keys.Enter)) {
		m.initAutoDraft()
		if key.Matches(msg, m.keys.Space) {
			m.repeatEnabled = true
			m.restoreAutoInterval()
			return m, m.ensureAutomaticIntervals()
		}
		m.repeatFocus = 1
		m.zone = ZoneMain
		m.restoreAutoInterval()
		return m, m.ensureAutomaticIntervals()
	}
	if key.Matches(msg, m.keys.Up) {
		m.moveDayRow(-1)
		return m, nil
	}
	if key.Matches(msg, m.keys.Down) {
		m.moveDayRow(1)
		return m, nil
	}
	if key.Matches(msg, m.keys.Top) {
		m.setDayRow(dayRows)
		return m, nil
	}
	if key.Matches(msg, m.keys.End) {
		m.setDayRow(dayRows - 1)
		return m, nil
	}
	if key.Matches(msg, m.keys.PageUp) {
		m.moveDayRow(-max(m.room.periods.height-1, 1))
		return m, nil
	}
	if key.Matches(msg, m.keys.PageDown) {
		m.moveDayRow(max(m.room.periods.height-1, 1))
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.Scan):
		// Scanning the day the cursor is on means loading it first.
		if m.selectedDay() != m.rooms.day {
			return m.applyDayCursor()
		}
		return m, m.beginScanPeriods()
	case key.Matches(msg, m.keys.Space):
		return m.selectPlannerDay()
	case key.Matches(msg, m.keys.Enter):
		return m.previewSide()
	}
	return m, nil
}

// keySeats handles the static seat list. j/k move through the numbering; enter
// asks the service what the seat is doing now and shows it in the content.
func (m *Model) keySeats(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Up):
		m.room.seats.Move(-1)
	case key.Matches(msg, m.keys.Down):
		m.room.seats.Move(1)
	case key.Matches(msg, m.keys.Top):
		m.room.seats.GotoTop()
	case key.Matches(msg, m.keys.End):
		m.room.seats.GotoBottom()
	case key.Matches(msg, m.keys.PageUp):
		m.room.seats.Page(-1)
	case key.Matches(msg, m.keys.PageDown):
		m.room.seats.Page(1)
	case key.Matches(msg, m.keys.Filter):
		return m.startFilter()
	case key.Matches(msg, m.keys.Space):
		return m.selectSeat()
	case key.Matches(msg, m.keys.Enter):
		return m.runSeatAction()
	}
	return m, nil
}

// runSeatAction is the seat panel's primary action: fetch what the seat is doing
// right now and show it in the content panel. Preparing the reservation is a
// separate step from there, so the list itself stays static and read-only.
func (m *Model) runSeatAction() (tea.Model, tea.Cmd) {
	if m.session.phase != phaseConfirmed {
		m.focusSide(PaneSeats)
		return m.keySession(tea.KeyMsg{Type: tea.KeyEnter})
	}
	if m.room.id == 0 {
		m.focusSide(PaneRooms)
		m.pushToast(sevInfo, "先打开一间自习室")
		return m, nil
	}
	if _, ok := m.selectedSeatNumber(); !ok {
		m.pushToast(sevInfo, "没有可查看的座位")
		return m, nil
	}
	m.focusZone(ZoneMain)
	slot, ok := m.room.currentSlot()
	if !ok {
		m.pushToast(sevWarn, "先在 %s 选择一个时段", paneTitle(PanePeriods))
		return m, nil
	}
	if _, cached := m.room.occupancyFor(slot.StartTime, slot.EndTime); cached {
		m.layout()
		return m, nil
	}
	return m, m.beginLoadSeats(slot)
}

// planSeatAction commits the seat the cursor is on: the seat map's and the preview
// card's "space". It goes through the same 选中/预订 decision as the seat list, so a
// seat that cannot be signed is remembered as a pre-order instead of failing to sign.
func (m *Model) planSeatAction() (tea.Model, tea.Cmd) {
	number, ok := m.selectedSeatNumber()
	if !ok {
		m.pushToast(sevInfo, "没有可选择的座位")
		return m, nil
	}
	return m.commitSeat(number)
}

// selectSlot moves the cursor without changing explicitly selected endpoints.
func (m *Model) selectSlot(index int) {
	if index < 0 || index >= len(m.room.slots) {
		return
	}
	if m.room.slotIndex != index && !m.room.periodExplicit {
		m.candidates = nil
		m.selection = nil
		m.genSelect++
		m.room.pendingSeat = ""
	}
	m.room.slotIndex = index
	m.syncLists()
}

// moveDayRow walks the planner and keeps the list window on the cursor, so a row
// is never selected while it is scrolled out of view.
func (m *Model) moveDayRow(delta int) {
	m.setDayRow(plannerRow(min(max(plannerPosition(m.days.cursor)+delta, 0), periodRows-1)))
}

func (m *Model) setDayRow(row int) {
	m.days.cursor = min(max(row, 0), periodRows-1)
	m.room.periods.cursor = plannerPosition(m.days.cursor)
	m.room.periods.clamp()
	m.layout()
}

// querySelectedPeriod loads the period in use unless it is cached.
func (m *Model) querySelectedPeriod() tea.Cmd {
	slot, ok := m.room.currentSlot()
	if !ok {
		m.pushToast(sevWarn, "尚未打开房间或当天没有可用时段")
		return nil
	}
	if _, cached := m.room.occupancyFor(slot.StartTime, slot.EndTime); cached {
		m.syncLists()
		return nil
	}
	m.syncLists()
	return m.beginLoadSeats(slot)
}

// --- scanning every period -------------------------------------------------

func (m *Model) beginScanPeriods() tea.Cmd {
	if m.room.scanning || m.room.loadingRoom || m.room.id == 0 || len(m.room.slots) == 0 {
		return nil
	}
	m.genScan++
	m.room.scanning = true
	m.room.scanDone = 0
	m.pushToast(sevInfo, "开始扫描 %d 个时段", len(m.room.slots))
	return m.cmdScanPeriod(m.genScan, 0)
}

// cmdScanPeriod fetches one period's occupancy. The scan is a chain of these, so
// it stays interruptible and the panel can show live progress.
func (m *Model) cmdScanPeriod(gen, index int) tea.Cmd {
	client, parent := m.client.Snapshot(), m.ctx
	slots := append([]chaoxing.Slot(nil), m.room.slots...)
	return func() tea.Msg {
		if index >= len(slots) {
			return periodScanMsg{gen: gen, index: index}
		}
		ctx, cancel := context.WithTimeout(parent, timeoutSeats)
		defer cancel()
		occupancy, err := client.SeatMap(ctx, slots[index].StartTime, slots[index].EndTime)
		return periodScanMsg{gen: gen, index: index, occupancy: occupancy, err: err}
	}
}

func (m *Model) onPeriodScan(msg periodScanMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.genScan {
		return m, nil
	}
	if msg.err != nil {
		m.room.scanning = false
		if errors.Is(msg.err, context.Canceled) {
			return m, nil
		}
		m.room.err = classify(msg.err)
		m.room.errLabel = "扫描中断"
		m.pushToast(sevErr, "扫描中断：%s", m.room.err.Short)
		return m, nil
	}
	m.room.remember(msg.occupancy)
	m.room.scanDone = msg.index + 1
	if msg.index+1 >= len(m.room.slots) {
		m.room.scanning = false
		m.pushToast(sevOK, "已扫描 %d 个时段", m.room.scanDone)
		return m, nil
	}
	return m, m.cmdScanPeriod(m.genScan, msg.index+1)
}

// weekdayNames labels the planner's rows. Sunday is first because Go's Weekday
// starts there.
var weekdayNames = [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

// Keep date offsets separate from display order: the automatic row is first.
func plannerPosition(row int) int {
	if row == dayRows {
		return 0
	}
	return row + 1
}
func plannerRow(position int) int {
	if position == 0 {
		return dayRows
	}
	return position - 1
}
