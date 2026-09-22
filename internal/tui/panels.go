package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"wfuseat/internal/chaoxing"
)

// The frame is a master-detail hierarchy, the way lazygit's is:
//
//	╭─ 状态 ──────────╮╭─ 房间详情 ─────────────────────╮
//	│选座客户端       ││名称  主校区 - 一楼 - 101自修室 │
//	│登录  已登录     ││容量  120                       │
//	╰─────────────────╯│                                │
//	╭─ [1] 房间 ──────╮│                                │
//	│▸ 6299 …       ● │╰────────────────────────────────╯
//	╰─────────── 1/2 ─╯╭─ [5] 选座预览 ─────────────────╮
//	╭─ [2] 时段 ──────╮│座位  024 · 主校区 - 一楼 …     │
//	│  19:00–19:30    ││状态  未提交（本地）            │
//	╰─────────── 1/2 ─╯╰────────────────────────────────╯
//	╭─ [3] 座位 ──────╮
//	│▸ 001   ● 空闲   │
//	╰────────── 1/120 ─╯
//
// The left column is the panel group: h/l choose which list is active, j/k move
// inside it, and every panel keeps its own size whether or not it is focused. The
// right column shows the active list's content, and enter moves there.
const (
	// statusPanelStatusMinRows is where the project panel can also afford its own
	// status line; below it the rows are better spent on the body.
	statusPanelStatusMinRows = 30
	detailPanelBodyRows      = 6
	// detailPanelBodyRowsEmpty is the card's height before anything is prepared.
	detailPanelBodyRowsEmpty = 3
)

// paneTitle is the panel title, with the key that focuses it: the panel's own
// number, which is why the project panel is [0].
func paneTitle(p Pane) string {
	return "[" + itoa(int(p)) + "] " + p.title()
}

// sidePanes are the left column's panels, in the order h/l walks them. The
// project panel comes first because a session starts there.
var sidePanes = [...]Pane{PaneStatus, PaneRooms, PanePeriods, PaneSeats, PaneRecords}

// sideIndex is a left panel's position in the h/l ring.
func sideIndex(p Pane) int {
	for i, side := range sidePanes {
		if side == p {
			return i
		}
	}
	return 0
}

// statusPanel is the frame's project panel, and the way into a session: its
// content beside it is the project information and the QR login. Like lazygit's
// Status panel it is short, so the lists below it get the rows.
func (m *Model) statusPanel() panelSlot {
	return panelSlot{
		owner: panelOwner{zone: ZoneSide, side: PaneStatus},
		title: paneTitle(PaneStatus),
		trail: m.sessionPhaseLabel,
		status: func() string {
			if _, height := m.frameSize(); height < statusPanelStatusMinRows {
				return ""
			}
			return "n 新增 · enter 打开账号详情"
		},
		body: m.accountRows,
	}
}

func (m *Model) dayOrDash() string {
	if m.rooms.day == "" {
		return "—"
	}
	return m.rooms.day
}

func (m *Model) periodOrDash() string {
	slot, ok := m.room.currentSlot()
	if !ok {
		return "—"
	}
	return slot.StartTime + "–" + slot.EndTime
}

// sidePanel is one list of the panel group.
func (m *Model) sidePanel(side Pane) panelSlot {
	slot := panelSlot{owner: panelOwner{zone: ZoneSide, side: side}, title: paneTitle(side)}
	switch side {
	case PaneStatus:
		return m.statusPanel()
	case PaneRooms:
		slot.trail, slot.count, slot.status, slot.body =
			m.roomsTrailing, m.listCount(&m.rooms.list), m.roomsStatusLine, m.roomsBodyLines
	case PanePeriods:
		slot.count, slot.status, slot.body =
			m.listCount(&m.room.periods), m.periodsStatusLine, m.periodsBodyLines
	case PaneSeats:
		slot.trail, slot.count, slot.status, slot.body =
			m.seatsTrailing, m.listCount(&m.room.seats), m.seatsStatusLine, m.seatsBodyLines
	case PaneRecords:
		slot.trail, slot.count, slot.status, slot.body =
			m.preordersTrailing, m.listCount(&m.preorders.list), m.preordersStatusLine, m.preordersBodyLines
	}
	return slot
}

// listCount is the "3 of 11" marker lazygit puts on a list panel's bottom
// border: where the window is, not how many items exist. It is evaluated at draw
// time because the window is sized by the layout pass.
func (m *Model) listCount(list *listView) func() string {
	return func() string {
		if list == nil {
			return ""
		}
		position, total := list.Scrolled()
		if total == 0 {
			return ""
		}
		return m.listPosition(position, total)
	}
}

// mainPanel is the right column's content: the detail of whatever the active left
// list is showing. Moving the cursor in the panel group updates this immediately,
// which is what makes the left column feel like a set of tabs rather than a menu.
func (m *Model) mainPanel() panelSlot {
	// The project panel's content is the login screen, and before a session exists
	// that is the content wherever the panel group is pointing.
	if m.side == PaneStatus || m.session.phase != phaseConfirmed {
		return m.statusContentPanel()
	}
	slot := panelSlot{owner: panelOwner{zone: ZoneMain, side: m.side}}
	switch m.side {
	case PaneRooms:
		slot.title = "房间详情"
		slot.trail = m.roomDetailTrail
		slot.status = m.roomDetailStatus
		slot.body = m.roomDetailBody
	case PanePeriods:
		slot.title = "时段详情"
		slot.trail = m.periodDetailTrail
		slot.status = m.periodDetailStatus
		slot.body = m.periodDetailBody
	case PaneRecords:
		slot.title = "预订详情"
		slot.trail = m.preordersDetailTrail
		slot.status = m.preordersDetailStatus
		slot.body = m.preordersDetailBody
	default:
		slot.title = "座位详情"
		slot.trail = m.seatDetailTrail
		slot.status = m.seatDetailStatus
		slot.body = m.seatDetailBody
	}
	return slot
}

// previewPanel is the always-visible card under the content: the prepared
// selection, the last write's verdict, and the next step. Keeping it on screen is
// the same choice lazygit makes with its command log: the answer to "what am I
// about to do?" never requires opening anything.
func (m *Model) previewPanel() panelSlot {
	return panelSlot{
		owner:  panelOwner{zone: ZonePreview},
		title:  paneTitle(PaneDetails),
		trail:  m.detailsTrailing,
		status: m.detailsStatusLine,
		body:   m.detailsBodyLines,
	}
}

func (m *Model) detailsTrailing() string {
	if m.selection != nil {
		if m.selection.Submitted {
			return "已提交"
		}
		return "已准备"
	}
	return ""
}

func (m *Model) detailsStatusLine() string {
	if m.auto.armed {
		return m.autoCountdown() + " · a 重排 · esc 取消"
	}
	if m.write.err != nil {
		return "上次提交失败：" + m.write.err.Short
	}
	if m.write.result != nil {
		return m.write.what + m.writeResultText()
	}
	if m.selection != nil {
		if m.selection.Submitted {
			return "enter 查看完整编码 · 服务端已确认"
		}
		return "enter 查看完整编码 · 未提交"
	}
	return ""
}

// writeResultText renders the service's own words about the last attempt.
func (m *Model) writeResultText() string {
	state := "未确认"
	if m.write.result.OK {
		state = "已确认"
	}
	return state + " · " + m.write.result.Message
}

// detailsBodyLines is the preview card: the signed selection if there is one, and
// otherwise the state of the walk with the next step spelled out.
func (m *Model) detailsBodyLines(width, height int, focused bool) []string {
	if m.db != nil {
		interval := m.periodOrDash()
		if m.repeatEnabled {
			interval = fmt.Sprintf("自动 · %s · 持续至取消", autoModes[m.repeatMode])
		}
		return m.detailRows(width, height, [][2]string{{"账号", m.accountLabel()}, {"候选顺序", strings.Join(m.candidates, " → ")}, {"时段", interval}, {"操作", "space 切换座位 · u 账号"}})
	}
	if m.selection != nil {
		return m.selectionDetailLines(width, height)
	}
	rows := [][2]string{
		{"房间", m.roomOrDash()},
		{"时段", m.periodOrDash()},
		{"容量", m.capacityOrDash()},
		{"空闲", m.occupancyDetail()},
	}
	if m.room.id == 0 {
		rows = rows[:2]
	}
	if seat, ok := m.selectedSeatState(); ok {
		rows = append(rows, [2]string{"座位", seat.Num + " · " + seat.Status.String()})
	}
	lines := m.detailRows(width, height, rows)
	if hint := m.detailsHint(); hint != "" && len(lines) < height {
		lines = append(lines, m.theme.FaintText.Render(m.clip(hint, width)))
	}
	return padList(lines, height)
}

// detailsHint answers "what now?" for whatever is still missing, so the frame
// always carries a next step even when nothing is loaded.
func (m *Model) detailsHint() string {
	switch {
	case m.session.phase != phaseConfirmed:
		return "按 enter 生成二维码登录"
	case m.room.id == 0:
		return "在 " + paneTitle(PaneRooms) + " 按 enter 打开房间"
	case len(m.room.slots) == 0:
		return "当天无可用时段 · 按 ] 看后一天"
	}
	if _, cached := m.room.currentOccupancy(); !cached {
		return "在 " + paneTitle(PanePeriods) + " 按 enter 查询该时段"
	}
	return ""
}

// selectionDetailLines is the preview once a selection exists. It fits the whole
// summary in the card's preferred height, and ends on the one fact that must
// never be missed.
func (m *Model) selectionDetailLines(width, height int) []string {
	summary := m.selection.Summary()
	signature := "已生成"
	if !summary.SignaturePrepared {
		signature = "未生成"
	}
	room := summary.Room
	if room == "" {
		room = "房间 " + itoa(summary.RoomID)
	}
	state, stateStyle := "未提交（本地）", m.theme.BadgeWarn
	if summary.Submitted {
		state, stateStyle = "已提交", m.theme.BadgeOK
	}
	rows := [][2]string{
		{"座位", summary.SeatNum + " · " + room},
		{"时段", summary.StartTime + "–" + summary.EndTime},
		{"日期", summary.Day},
		{"签名", signature},
		{"状态", state},
	}
	if line := m.detailsAutoLine(); line != "" {
		rows = append(rows, [2]string{"定时", line})
	}
	if result := m.writeBlock(); result != "" {
		rows = append(rows, [2]string{"结果", result})
	}
	lines := m.detailRows(width, height, rows)
	for i, row := range rows {
		if row[0] != "状态" || i >= len(lines) {
			continue
		}
		labelWidth := min(8, max(width/4, 5))
		lines[i] = m.fit(
			m.theme.Label.Render(m.column("状态", labelWidth))+" "+
				stateStyle.Render(m.clip(state, max(width-labelWidth-1, 4))), width)
	}
	return padList(lines, height)
}

// detailsAutoLine is the scheduled request's own row on the preview card.
func (m *Model) detailsAutoLine() string {
	if !m.auto.armed && m.auto.result == "" {
		return ""
	}
	return m.autoCountdown()
}

// detailRows renders "label value" rows, one per line, clipped to the width.
func (m *Model) detailRows(width, height int, rows [][2]string) []string {
	labelWidth := min(8, max(width/4, 5))
	lines := make([]string, 0, min(height, len(rows)))
	for _, row := range rows {
		if len(lines) >= height {
			break
		}
		lines = append(lines, m.fit(
			m.theme.Label.Render(m.column(row[0], labelWidth))+" "+
				m.theme.Value.Render(m.clip(row[1], max(width-labelWidth-1, 4))), width))
	}
	return lines
}

func (m *Model) roomOrDash() string {
	if m.room.id == 0 {
		return "—"
	}
	name := m.room.name
	if name == "" {
		name = "房间 " + itoa(m.room.id)
	}
	return fmt.Sprintf("%s · ID %d", name, m.room.id)
}

func (m *Model) capacityOrDash() string {
	if m.room.id == 0 {
		return "—"
	}
	return itoa(m.room.capacity)
}

// occupancyDetail reports the current period's picture. An unqueried period says
// so rather than showing zeroes, which would read as a full room.
func (m *Model) occupancyDetail() string {
	occupancy, ok := m.room.currentOccupancy()
	if !ok {
		return "—"
	}
	return fmt.Sprintf("%d 空闲 / %d 占用 / %d 不可选",
		occupancy.Free, occupancy.Occupied, occupancy.Disabled+occupancy.Unavailable)
}

// selectedSeatState returns the seat under the cursor in the seat list.
func (m *Model) selectedSeatState() (chaoxing.SeatState, bool) {
	states := m.visibleSeatStates()
	index, ok := m.room.seats.Selected()
	if !ok || index >= len(states) {
		return chaoxing.SeatState{}, false
	}
	return states[index], true
}

// --- the right column's content --------------------------------------------

func (m *Model) roomDetailTrail() string {
	room, ok := m.selectedRoom()
	if !ok {
		return ""
	}
	return itoa(room.ID)
}

func (m *Model) roomDetailStatus() string {
	if m.filter.active {
		return "/ " + m.filter.input.Value()
	}
	room, ok := m.selectedRoom()
	if !ok {
		return "未选中房间"
	}
	if !room.Selectable {
		return m.badge("当前不可预约", sevNone)
	}
	if m.room.id == room.ID {
		return "enter 重新打开该房间"
	}
	return "enter 打开该房间"
}

func (m *Model) roomDetailBody(width, height int, focused bool) []string {
	room, ok := m.selectedRoom()
	// Whatever happened when the room was opened is reported here, because this
	// is where the keyboard lands after opening it: a failure that only showed in
	// the list panel would leave the user staring at a silent detail card.
	// A failure belongs to the room the open was attempted on, so it is reported
	// here -- the panel the keyboard stays on -- and not only in the list.
	if room, ok := m.selectedRoom(); ok && room.ID == m.room.attempt && m.room.attempt != 0 {
		switch {
		case m.room.unsupported != nil:
			return toLines(m.stateView(m.glyphs.Ring, sevWarn,
				m.room.unsupported.Short, m.room.unsupported.Detail, m.room.unsupported.Hint,
				width, height), height)
		case m.room.err != nil:
			return toLines(m.errorView(m.room.err, width, height), height)
		}
	}
	if !ok {
		return toLines(m.emptyView("没有选中的房间",
			"房间列表载入后按 j/k 选择。", width, height), height)
	}
	state := "可在线预约"
	if !room.Selectable {
		state = "当前不可预约"
	}
	rows := [][2]string{
		{"名称", room.Name},
		{"ID", itoa(room.ID)},
		{"容量", itoa(room.Capacity)},
		{"状态", state},
	}
	if m.room.id == room.ID {
		rows = append(rows, [2]string{"已打开", "是 · " + itoa(len(m.room.slots)) + " 个时段"})
		if slot, ok := m.room.currentSlot(); ok {
			when := slot.StartTime + "–" + slot.EndTime
			if occupancy, cached := m.room.currentOccupancy(); cached {
				rows = append(rows, [2]string{"首个时段", fmt.Sprintf("%s · %d 空闲 / %d 占用",
					when, occupancy.Free, occupancy.Occupied)})
			} else {
				rows = append(rows, [2]string{"首个时段", when + " · 尚未查询"})
			}
		}
		if status, opensAt, ok := m.client.ReserveWindow(); ok && status != "" && status != "AVAILABLE" {
			note := status
			if !opensAt.IsZero() {
				note += " · " + opensAt.Format("15:04:05") + " 开放"
			}
			rows = append(rows, [2]string{"预约窗口", note})
		}
	}
	lines := m.detailRows(width, height, rows)
	if hint := m.roomDetailHint(room.Selectable); hint != "" && len(lines) < height {
		lines = append(lines, m.theme.FaintText.Render(m.clip(hint, width)))
	}
	return padList(lines, height)
}

func (m *Model) roomDetailHint(selectable bool) string {
	if !selectable {
		return "该房间当前不可在线预约，按 j/k 选择其他房间。"
	}
	if m.room.id == 0 {
		return "按 enter 打开该房间并载入时段。"
	}
	if len(m.room.slots) == 0 {
		return "该房间当天没有可用时段：按 ] 切到后一天规划，或请选择其他日期。"
	}
	if _, cached := m.room.currentOccupancy(); !cached {
		return "到 " + paneTitle(PanePeriods) + " 按 enter 查询时段，座位见 " + paneTitle(PaneSeats) + "。"
	}
	return "座位见 " + paneTitle(PaneSeats) + "；换时段用 " + paneTitle(PanePeriods) + "。"
}

func (m *Model) periodDetailTrail() string {
	return m.dayOrDash()
}

func (m *Model) periodDetailStatus() string {
	if m.autoPreview() && m.repeatMode == 2 {
		if m.repeatFocus == 4 {
			return "l/→ 进入时段 · j/k 选星期 · space 启停 · tab 配置 · 3 选座"
		}
		if m.repeatFocus == 0 {
			return "h/← 返回星期 · j/k 选时段 · space 端点 · enter 下一天 · 3 选座"
		}
		return "tab 切换配置/星期/时段 · h/l 左右选择 · space 确认 · esc 返回"
	}
	if m.autoPreview() {
		return "tab 切换区域 · 方向键移动 · space 选择 · enter 编辑/继续 · esc 返回"
	}
	if m.room.id == 0 {
		return "只读 · 尚未打开自习室"
	}
	if len(m.room.slots) == 0 {
		return "该日没有可用时段"
	}
	if _, cached := m.room.currentOccupancy(); cached {
		return "h/l 切列 · j/k 移动 · space 切换端点 · enter 选座 · esc 返回"
	}
	return "hjkl 移动 · space 选择/取消首尾（最多两个）· enter 选座"
}

// periodDetailBody is the content of the day planner: the day's periods laid out
// as a tiled grid so the whole day is visible at once, or the repeat option
// that decides when a scheduled request fires.
func (m *Model) periodDetailBody(width, height int, focused bool) []string {
	if m.autoPreview() {
		return m.automaticBody(width, height, focused)
	}
	return m.periodSelectionBody(width, height, focused)
}
func (m *Model) periodSelectionBody(width, height int, focused bool) []string {
	if m.room.unsupported != nil {
		return toLines(m.stateView(m.glyphs.Ring, sevWarn,
			m.room.unsupported.Short, m.room.unsupported.Detail, m.room.unsupported.Hint,
			width, height), height)
	}
	if m.room.err != nil {
		return toLines(m.errorView(m.room.err, width, height), height)
	}
	if m.room.id == 0 {
		return toLines(m.readOnlyView("只读 · 尚未打开自习室",
			"在 "+paneTitle(PaneRooms)+" 按 enter 打开一间自习室后，这里平铺该日全部时段。",
			width, height), height)
	}
	if m.room.loadingRoom {
		return toLines(m.loadingView("正在打开房间…", width, height), height)
	}
	if len(m.room.slots) == 0 {
		return toLines(m.readOnlyView("该日没有可用时段",
			"按 ] 换一天，请选择其他日期，或在 "+paneTitle(PaneRooms)+" 改选自习室。",
			width, height), height)
	}
	return m.periodGridLines(width, height)
}

// measureGrid records how many cells the content grid fits. The movement keys and
// the renderer both read it, so a step can never disagree with what is drawn.
func (m *Model) measureGrid(slot panelSlot) {
	width := max(slot.interiorW, 1)
	var columns int
	switch slot.owner.side {
	case PaneSeats:
		// A seat cell is its number plus the state glyph, and a few of them
		// across reads like a seat map.
		columns = max(width/seatCellWidth, 1)
	default:
		m.periodWidth = width
		columns = 1
	}
	if columns < 1 {
		columns = 1
	}
	m.gridCols = columns
}

// periodGridColumns is how many period cells are laid out side by side.
func (m *Model) periodGridColumns() int { return 1 }

type periodColumn struct {
	title   string
	indices []int
}

func (m *Model) periodGroups(width int) []periodColumn {
	count := 1
	if width >= 72 {
		count = 2
	}
	if width >= 108 {
		count = 3
	}
	groups := []periodColumn{{title: "全天"}}
	if count == 2 {
		groups = []periodColumn{{title: "上午"}, {title: "下午 / 晚间"}}
	}
	if count == 3 {
		groups = []periodColumn{{title: "上午"}, {title: "下午"}, {title: "晚间"}}
	}
	for i, slot := range m.room.slots {
		col := 0
		if count > 1 && slot.StartTime >= "12:00" {
			col = 1
		}
		if count == 3 && slot.StartTime >= "18:00" {
			col = 2
		}
		groups[col].indices = append(groups[col].indices, i)
	}
	out := []periodColumn{}
	for _, g := range groups {
		if len(g.indices) > 0 {
			out = append(out, g)
		}
	}
	return out
}
func (m *Model) movePeriodColumn(delta int) {
	width := m.periodWidth
	if width == 0 {
		width, _ = m.frameSize()
	}
	groups := m.periodGroups(width)
	if len(groups) < 2 {
		m.selectSlot(m.room.slotIndex + delta)
		return
	}
	for col, g := range groups {
		for row, index := range g.indices {
			if index == m.room.slotIndex {
				next := col + delta
				if next >= 0 && next < len(groups) {
					m.selectSlot(groups[next].indices[min(row, len(groups[next].indices)-1)])
				}
				return
			}
		}
	}
}
func (m *Model) periodGridLines(width, height int) []string {
	if height <= 0 {
		return nil
	}
	summary := "空格选择首尾 · h/l 切列 · j/k 移动"
	if len(m.room.endpoints) > 0 {
		if slot, ok := m.room.currentSlot(); ok {
			summary = fmt.Sprintf("已选 %s–%s · %d/2 端点", slot.StartTime, slot.EndTime, len(m.room.endpoints))
		}
	}
	lines := []string{m.theme.Committed.Render(m.clip(summary, width))}
	if height == 1 {
		return lines
	}
	groups := m.periodGroups(width)
	if len(groups) == 0 {
		return padList(lines, height)
	}
	cellWidth := max((width-3*(len(groups)-1))/len(groups), 1)
	headers := []string{}
	for _, g := range groups {
		headers = append(headers, m.theme.Heading.Render(m.fit(fmt.Sprintf("%s · %d", g.title, len(g.indices)), cellWidth)))
	}
	lines = append(lines, strings.Join(headers, "   "))
	body := height - 2
	if body <= 0 {
		return lines
	}
	first := 0
	for _, g := range groups {
		for row, index := range g.indices {
			if index == m.room.slotIndex && row >= body {
				first = row - body + 1
			}
		}
	}
	for row := first; len(lines) < height; row++ {
		parts := []string{}
		any := false
		for _, g := range groups {
			if row < len(g.indices) {
				parts = append(parts, m.periodListCell(g.indices[row], cellWidth))
				any = true
			} else {
				parts = append(parts, strings.Repeat(" ", cellWidth))
			}
		}
		if !any {
			break
		}
		lines = append(lines, strings.Join(parts, "   "))
	}
	return padList(lines, height)
}
func (m *Model) periodListCell(i, width int) string {
	lo, hi := -1, -1
	for _, n := range m.room.endpoints {
		if lo < 0 || n < lo {
			lo = n
		}
		if n > hi {
			hi = n
		}
	}
	slot := m.room.slots[i]
	marker, label := "[ ]", ""
	style := m.theme.RowNormal
	switch {
	case i == lo && lo == hi:
		marker, label = "[S]", "单段"
	case i == lo:
		marker, label = "[S]", "起点"
	case i == hi:
		marker, label = "[E]", "终点"
	case lo >= 0 && i > lo && i < hi:
		marker, label = "[=]", "区间内"
	}
	if label != "" {
		style = m.theme.Committed
	}
	if i == lo || i == hi {
		style = style.Foreground(m.theme.Success).Bold(true)
	}
	status := "未查询"
	if occupancy, ok := m.room.occupancyFor(slot.StartTime, slot.EndTime); ok {
		status = fmt.Sprintf("空闲 %d/%d", occupancy.Free, occupancy.Total())
		if occupancy.Free == 0 {
			status = "已满"
		}
	}
	cursor := " "
	if i == m.room.slotIndex && (!m.autoPreview() || m.repeatFocus == 0) {
		cursor = ">"
		style = style.Background(m.theme.SelectedBg).Bold(true)
	}
	text := fmt.Sprintf("%s %s %s–%s  %-3s", cursor, marker, slot.StartTime, slot.EndTime, label)
	if width >= 44 {
		text = m.fit(text, width-lipgloss.Width(status)-2) + "  " + status
	}
	return style.Render(m.fit(m.clip(text, width), width))
}

// occupancyBar draws how full a period is, with light block characters.
func (m *Model) occupancyBar(occupancy *chaoxing.Occupancy, width int) string {
	if occupancy == nil || width <= 0 {
		return strings.Repeat("·", max(width, 1))
	}
	total := max(occupancy.Total(), 1)
	used := occupancy.Occupied + occupancy.Disabled + occupancy.Unavailable
	filled := used * width / total
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("·", width-filled)
}

func occupancyFor(m *Model, slot chaoxing.Slot) *chaoxing.Occupancy {
	occupancy, _ := m.room.occupancyFor(slot.StartTime, slot.EndTime)
	return occupancy
}

// seatDetailTrail is the seat the map is on.
func (m *Model) seatDetailTrail() string {
	number, ok := m.selectedSeatNumber()
	if !ok {
		return ""
	}
	return number
}

func (m *Model) seatDetailStatus() string {
	switch {
	case m.room.loadingSeats:
		return m.spinner.View() + " 正在查询真实占用…"
	case m.room.id == 0:
		return "只读 · 未选择自习室"
	case len(m.room.slots) == 0:
		return "只读 · 该日没有可用时段"
	}
	// How the map is arranged, and how big it is, so a scrolled plan is not mistaken
	// for a small room.
	geometry := ""
	if size := m.seatPlanSize(m.seatPlan()); size != "" {
		geometry = size + " · "
	}
	switch {
	case !m.client.CanSign():
		return geometry + "hjkl 移动 · space 预订该座位 · esc 返回"
	case m.seatCursorKnown():
		return geometry + "hjkl 移动 · space 选中该座位 · enter 下一项 · esc 返回"
	}
	return geometry + "hjkl 移动 · space 选中并查询 · esc 返回"
}

// seatCursorKnown reports whether the map has occupancy to draw.
func (m *Model) seatCursorKnown() bool {
	_, cached := m.room.currentOccupancy()
	return cached
}

// seatDetailBody is the content of the seat panel: the seat map, with the seat
// under the cursor reported above it and the room's own rows below it.
func (m *Model) seatDetailBody(width, height int, focused bool) []string {
	known := m.staticSeatCount() > 0
	if m.room.unsupported != nil && !known {
		return toLines(m.stateView(m.glyphs.Ring, sevWarn,
			m.room.unsupported.Short, m.room.unsupported.Detail, m.room.unsupported.Hint,
			width, height), height)
	}
	if m.room.err != nil && !known {
		return toLines(m.errorView(m.room.err, width, height), height)
	}
	if m.room.id == 0 {
		return toLines(m.readOnlyView("只读 · 未选择自习室",
			"在 "+paneTitle(PaneRooms)+" 按 space 选中一间自习室，再在 "+
				paneTitle(PaneSeats)+" 按 space 选中座位。", width, height), height)
	}
	// The map is the room's numbering, so it is there whatever this day answered.
	// What could not be loaded, and why choosing a seat is a pre-order, is said
	// once, plainly, above it rather than replacing it.
	lines := make([]string, 0, height)
	switch {
	case m.room.err != nil:
		lines = append(lines, m.fit(m.theme.BadgeErr.Render(
			m.roomErrorLabel()+"："+m.room.err.Short+" · 下列为已知座位 · space 预订"), width))
	case m.room.unsupported != nil:
		lines = append(lines, m.fit(m.theme.BadgeWarn.Render(
			m.room.unsupported.Short+" · 下列为已知座位"), width))
	case len(m.room.slots) == 0:
		lines = append(lines, m.fit(m.theme.BadgeWarn.Render(
			"该日没有可用时段 · space 仍可预订"), width))
	case !m.client.CanSign():
		lines = append(lines, m.fit(m.theme.BadgeWarn.Render(
			"只读 · 可预订："+m.signReason()+" · 按 space 预订座位"), width))
	}
	detail := m.seatDetailRows(mustSeatNumber(m))
	reserved := 0
	if height >= 8 {
		reserved = min(len(detail)+1, height/2)
	}
	lines = append(lines, m.seatGridBody(width, max(height-reserved-len(lines), 1), focused)...)
	if reserved > 0 {
		// The rule doubles as the map's colour key, so the marks need no guessing.
		rule := strings.Repeat(m.glyphs.Rule, width)
		if legend := m.seatGridLegend(); lipgloss.Width(legend)+4 <= width {
			pad := width - lipgloss.Width(legend) - 3
			rule = strings.Repeat(m.glyphs.Rule, pad) + " " + legend + " "
		}
		lines = append(lines, m.theme.FaintText.Render(m.clip(rule, width)))
		for _, row := range detail {
			if len(lines) >= height {
				break
			}
			lines = append(lines, m.fit(
				m.theme.Label.Render(m.column(row[0], 8))+" "+
					m.theme.Value.Render(m.clip(row[1], max(width-9, 4))), width))
		}
	}
	return padList(lines, height)
}

// mustSeatNumber is the seat under the cursor, or an empty string when there is
// none.
func mustSeatNumber(m *Model) string {
	number, _ := m.selectedSeatNumber()
	return number
}

// seatDetailRows is everything the service says about this seat, in the order a
// person reads it: where it is, and what it is doing now.
func (m *Model) seatDetailRows(number string) [][2]string {
	rows := [][2]string{}
	if spot, ok := m.seatSpot(number); ok {
		rows = append(rows, [2]string{"位置", seatSpotText(spot)})
		rows = append(rows, [2]string{"房间位次", fmt.Sprintf("第 %d / %d 个", spot.Ordinal, m.staticSeatCount())})
		if spot.Paused {
			rows = append(rows, [2]string{"备注", "房间标记为暂停预约"})
		}
		if len(spot.Labels) > 0 {
			labels := make([]string, 0, len(spot.Labels))
			for _, label := range spot.Labels {
				labels = append(labels, itoa(label))
			}
			rows = append(rows, [2]string{"区域标签", strings.Join(labels, ", ")})
		}
	}
	if m.room.hasLayout {
		rows = append(rows, [2]string{"布局", m.room.layout.Mode})
	}
	// What the service's own seat listing carried, when there was one. A room whose
	// seats come with coordinates says so here instead of the tool inventing a
	// layout from fields it never received.
	if keys := m.client.GridKeys(); len(keys) > 0 {
		rows = append(rows, [2]string{"网格字段", strings.Join(keys, ", ")})
	}
	if coords := m.client.GridCoordKeys(); len(coords) > 0 {
		note := "（未用于排布）"
		if m.seatPlan() != nil {
			note = "（座位图按它排布）"
		}
		rows = append(rows, [2]string{"坐标字段", strings.Join(coords, ", ") + note})
	}
	rows = append(rows,
		[2]string{"自习室", m.room.name},
		[2]string{"房间 ID", itoa(m.room.id)},
	)
	if slot, ok := m.room.currentSlot(); ok {
		rows = append(rows, [2]string{"时段", slot.StartTime + "–" + slot.EndTime + "（当前时段）"})
	}
	if occupancy, cached := m.room.currentOccupancy(); cached {
		rows = append(rows, [2]string{"该时段", fmt.Sprintf("%d 空闲 / %d 占用 / %d 不可选 · 共 %d",
			occupancy.Free, occupancy.Occupied, occupancy.Disabled+occupancy.Unavailable, occupancy.Total())})
	}
	if notes := m.seatPeriodNotes(number); notes != "" {
		rows = append(rows, [2]string{"其他时段", notes})
	}
	if clock := m.serverClock(); clock != "" {
		rows = append(rows, [2]string{"查询于", clock + "（服务器时间）"})
	}
	return rows
}

// seatSpotText renders where a seat is from the room's own data: the position the
// service gave when it gave one, otherwise its place in the listing. A room that
// reports neither says so rather than implying a coordinate.
func seatSpotText(spot chaoxing.SeatSpot) string {
	switch spot.CoordSource {
	case "x/y":
		return fmt.Sprintf("x=%d y=%d（服务端坐标）", spot.Col, spot.Row)
	case "row/col":
		return fmt.Sprintf("row=%d col=%d（服务端坐标）", spot.Row, spot.Col)
	}
	switch {
	case spot.GridOrdinal > 0:
		return fmt.Sprintf("网格第 %d 位 / 共 %d 位", spot.GridOrdinal, spot.GridTotal)
	case spot.GridTotal > 0:
		return "网格中未列出"
	default:
		return "列表模式（房间未提供坐标）"
	}
}

// --- utility views ----------------------------------------------------------

// formPanel is the whole-frame view of the prepared signature form. The encoding
// is long and scrollable, so it gets the frame to itself rather than a corner.
func (m *Model) formPanel() panelSlot {
	return panelSlot{
		owner:  panelOwner{zone: ZoneView, index: 0},
		title:  "表单编码",
		trail:  func() string { return "只读" },
		status: func() string { return "enter 提交预约 · ↑/↓ 滚动 · " + notSubmittedNote },
		body:   m.selectionFormBody,
	}
}

// keyPreview handles the preview card. enter opens the full encoding when there
// is one to show; otherwise the card is informational.
func (m *Model) keyPreview(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.db != nil {
		if key.Matches(msg, m.keys.Space) || key.Matches(msg, m.keys.Enter) {
			return m.beginScheduledJob()
		}
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.Space):
		// space commits: with nothing prepared it prepares the seat the map is
		// on; with a preview it asks to submit it, which prints the request first.
		if m.selection == nil {
			return m.planSeatAction()
		}
		return m.beginSubmit()
	case key.Matches(msg, m.keys.Enter):
		// enter goes a level deeper: the encoding that would be sent.
		if m.selection == nil {
			return m.planSeatAction()
		}
		return m, m.openOverlay(OverlayForm)
	}
	return m, nil
}
