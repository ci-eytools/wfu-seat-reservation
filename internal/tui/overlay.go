package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// policyNote is shown in the help overlay so the tool's boundary is never a
// surprise: queries are read-only, and the one write is confirmed first and
// printed in full before it is sent.
const policyNote = "查询只读；a 创建自动预订后会在计划时刻真实执行。最多三座，按顺序尝试；仅明确拒绝才换座，结果未知暂停。u 切换账号。"

// layoutNote explains the frame, because a panel stack is only obvious once
// somebody says that every panel is on screen at the same time.
const layoutNote = "0–4 选择左侧面板，5 预览；Tab 切换左栏/详情/预览。左栏 hjkl 导航，右侧座位图 hjkl 二维移动；空格选中/取消，Enter 进入详情，Esc 返回。时段第一项为自动，与具体日期互斥；右侧 Tab 切换规则、开始日期、星期和时段。账号配置直接在右侧编辑，Ctrl+S 保存。"

// helpGroups is the full, grouped shortcut list for the help overlay. The
// context group follows whatever owns the keyboard, so the modal answers "what
// can I do here?" and not just "what keys exist?".
func (m *Model) helpGroups() []bindingGroup {
	groups := m.keys.globalGroups()
	switch m.overlay {
	case OverlayLogs:
		return append(groups, bindingGroup{Title: "诊断", Keys: []key.Binding{
			m.keys.Up, m.keys.Down, m.keys.Enter, m.keys.Top, m.keys.End}})
	case OverlayForm:
		return append(groups, bindingGroup{Title: "表单", Keys: []key.Binding{
			m.keys.Up, m.keys.Down, m.keys.Top, m.keys.End}})
	}
	switch {
	case m.zone == ZoneMain && (m.side == PanePeriods || m.side == PaneSeats):
		groups = append(groups, bindingGroup{Title: "选择", Keys: []key.Binding{
			m.keys.Left, m.keys.Right, m.keys.Up, m.keys.Down,
			m.keys.Space, m.keys.Enter, m.keys.Back}})
	case m.zone == ZoneMain:
		groups = append(groups, bindingGroup{Title: "内容", Keys: []key.Binding{
			m.keys.Enter, m.keys.Back}})
	case m.zone == ZonePreview:
		groups = append(groups, bindingGroup{Title: "选座预览", Keys: []key.Binding{
			m.keys.Enter, m.keys.Back}})
	case m.side == PaneRooms:
		groups = append(groups, bindingGroup{Title: "房间面板", Keys: []key.Binding{
			m.keys.Up, m.keys.Down, m.keys.Enter, m.keys.Filter}})
	case m.side == PanePeriods:
		groups = append(groups, bindingGroup{Title: "时段面板", Keys: []key.Binding{
			m.keys.Down, m.keys.Up, m.keys.Enter, m.keys.Scan}})
	case m.session.phase != phaseConfirmed:
		groups = append(groups, bindingGroup{Title: "登录", Keys: []key.Binding{
			m.keys.Enter, m.keys.Refresh}})
	default:
		groups = append(groups, bindingGroup{Title: "座位面板", Keys: []key.Binding{
			m.keys.Down, m.keys.Up, m.keys.Enter, m.keys.Filter}})
	}
	return groups
}

// overlayView renders a modal layer. The content area is replaced rather than
// composited: a terminal has no z-buffer, so drawing over live content would
// smear it. Clearing to a centred box is both correct and calmer to read.
func (m *Model) overlayView() string {
	width, height := m.width, m.height
	var box string
	switch m.overlay {
	case OverlayHelp:
		box = m.helpOverlay(width, height)
	case OverlayConfirm:
		box = m.confirmOverlay()
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// overlayChrome is the horizontal space the overlay frame adds around its
// content: one border cell on each side. lipgloss.Style.Width already accounts
// for padding, so only the border has to be subtracted.
const overlayChrome = 2

// overlayPadX is the horizontal padding the overlay style applies on each side.
// lipgloss.Style.Width includes padding, so the usable text area is narrower than
// the box's declared width.
const overlayPadX = 2

// wrapText wraps text to width cells, filling each line as far as it can. The UI
// text is mostly CJK, which has no spaces to break on, so a word wrapper would
// not help; measuring cells directly also avoids the ragged breaks that a
// rune-counting wrapper produces with wide characters.
func wrapText(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	var (
		lines   []string
		current strings.Builder
		used    int
	)
	for _, r := range s {
		cells := ansi.StringWidth(string(r))
		if used+cells > width && used > 0 {
			lines = append(lines, current.String())
			current.Reset()
			used = 0
		}
		current.WriteRune(r)
		used += cells
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return lines
}

func (m *Model) helpOverlay(width, height int) string {
	box := clampInt(width-overlayChrome, 12, 96)
	inner := max(box-2*overlayPadX, 8)

	lines := []string{
		m.theme.OverlayTitle.Render("快捷键"),
		"",
	}
	for _, wrapped := range wrapText(policyNote, inner) {
		lines = append(lines, m.theme.OverlayHelp.Render(wrapped))
	}
	for _, wrapped := range wrapText(layoutNote, inner) {
		lines = append(lines, m.theme.OverlayHelp.Render(wrapped))
	}
	lines = append(lines, "")

	const keyCol = 14
	for _, group := range m.helpGroups() {
		lines = append(lines, m.theme.Heading.Render(m.clip(group.Title, inner)))
		for _, b := range group.Keys {
			h := b.Help()
			descWidth := max(inner-keyCol-2, 4)
			lines = append(lines, "  "+
				m.theme.Accent.Render(m.fit(h.Key, keyCol))+
				m.theme.MutedText.Render(m.clip(h.Desc, descWidth)))
		}
		lines = append(lines, "")
	}

	// Window the content so a short terminal still shows a usable box. The box
	// frame costs four cells (border plus padding, top and bottom), and the
	// scroll indicator needs one line inside it.
	maxLines := max(height-4, 3)
	visible := lines
	if len(lines) > maxLines {
		bodyLines := max(maxLines-1, 1)
		m.helpTop = clampInt(m.helpTop, 0, max(len(lines)-bodyLines, 0))
		end := min(m.helpTop+bodyLines, len(lines))
		visible = append(append([]string{}, lines[m.helpTop:end]...),
			m.theme.FaintText.Render(
				m.clip(fmt.Sprintf("  ↑/↓ 滚动 · %d/%d", end, len(lines)), inner)))
	}

	body := make([]string, 0, len(visible))
	for _, line := range visible {
		body = append(body, m.clip(line, inner))
	}
	return m.theme.Overlay.Border(m.glyphs.Border).Width(box).Render(strings.Join(body, "\n"))
}

func (m *Model) confirmOverlay() string {
	box := clampInt(m.width-overlayChrome, 12, 64)
	inner := max(box-2*overlayPadX, 8)

	lines := []string{
		m.theme.OverlayTitle.Render(m.confirm.prompt),
		"",
	}
	if m.confirm.detail != "" {
		lines = append(lines, m.theme.MutedText.Render(m.clip(m.confirm.detail, inner)), "")
	}
	if m.confirm.request != "" {
		// A write is the one action that changes the service, so the request is
		// printed rather than summarised: method, path, source and body. The query
		// string is broken at parameter boundaries, because a signature split
		// mid-token cannot be compared against anything.
		for _, raw := range strings.Split(m.confirm.request, "\n") {
			body, isParams := strings.CutPrefix(raw, "参数  ")
			if !isParams {
				for _, wrapped := range wrapText(raw, inner) {
					lines = append(lines, m.theme.OverlayHelp.Render(wrapped))
				}
				continue
			}
			lines = append(lines, m.theme.OverlayHelp.Render("参数"))
			for _, line := range strings.Split(breakQuery(body, max(inner-4, formMinBreak)), "\n") {
				lines = append(lines, m.theme.OverlayHelp.Render("  "+line))
			}
		}
		lines = append(lines, "")
	}
	lines = append(lines,
		m.confirmButtons(),
		m.theme.FaintText.Render(m.clip("←/→ 或 h/l 切换 · enter 确认 · esc 取消", inner)),
	)

	body := make([]string, 0, len(lines))
	for _, line := range lines {
		body = append(body, m.clip(line, inner))
	}
	return m.theme.Overlay.Border(m.glyphs.Border).Width(box).Render(strings.Join(body, "\n"))
}

// confirmButtons renders the two choices. The safe choice is focused first, so a
// stray Enter cancels instead of confirming.
func (m *Model) confirmButtons() string {
	label := m.confirm.confirm
	if label == "" {
		label = "确认"
	}
	yes := "[ " + label + " ]"
	no := "[ 取消 ]"
	if m.confirm.focusYes {
		return m.theme.BadgeErr.Bold(true).Render(yes) + "   " + m.theme.MutedText.Render(no)
	}
	return m.theme.MutedText.Render(yes) + "   " + m.theme.BadgeOK.Bold(true).Render(no)
}

func (m *Model) handleHelpKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Back), key.Matches(msg, m.keys.Help), msg.Type == tea.KeyEsc:
		m.overlay = OverlayNone
	case key.Matches(msg, m.keys.Up):
		m.helpTop--
	case key.Matches(msg, m.keys.Down):
		m.helpTop++
	case msg.Type == tea.KeyPgUp:
		m.helpTop -= 10
	case msg.Type == tea.KeyPgDown:
		m.helpTop += 10
	case key.Matches(msg, m.keys.Quit):
		m.overlay = OverlayNone
		return m.requestQuit()
	}
	if m.helpTop < 0 {
		m.helpTop = 0
	}
	return m, nil
}

func (m *Model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.overlay = OverlayNone
		return m, nil
	case tea.KeyLeft, tea.KeyRight, tea.KeyTab, tea.KeyShiftTab:
		m.confirm.focusYes = !m.confirm.focusYes
		return m, nil
	case tea.KeyEnter:
		if !m.confirm.focusYes {
			m.overlay = OverlayNone
			return m, nil
		}
		return m.runConfirm()
	}
	switch msg.String() {
	case "h":
		m.confirm.focusYes = true
	case "l":
		m.confirm.focusYes = false
	case "y", "Y":
		m.confirm.focusYes = true
		return m.runConfirm()
	case "n", "N":
		m.overlay = OverlayNone
		return m, nil
	}
	return m, nil
}

// runConfirm executes the pending action against the current model.
func (m *Model) runConfirm() (tea.Model, tea.Cmd) {
	action := m.confirm.action
	m.overlay = OverlayNone
	m.confirm = confirmState{}

	switch action {
	case confirmQuit:
		m.quitSkip = true
		m.cancel()
		return m, tea.Quit
	case confirmReserve:
		return m, m.beginWrite(action)
	case confirmAutoReserve:
		return m, m.armAutoReserve()
	case confirmDeleteAccount:
		return m.deleteAccount()
	case confirmSchedule:
		return m, m.saveScheduledJob()
	case confirmCancelJob:
		return m, m.cancelJob()
	case confirmCancelPreorder:
		item, ok := m.selectedPreorder()
		if !ok {
			return m, nil
		}
		return m, m.removePreorder(item.ID)
	}
	return m, nil
}
