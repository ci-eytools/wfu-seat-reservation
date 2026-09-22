package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Layout constants. Panels draw their own frames, so the header and footer are one
// line each, and a hard minimum keeps the panels usable.
const (
	// Preferred interior widths of the panel group. The right column absorbs the
	// remainder, so these only bias the split: the group is wide enough for a row
	// to carry its own state beside its name, and it narrows in two steps rather
	// than squeezing the content out of the frame.
	leftPanelWidthWide   = 48
	leftPanelWidthMid    = 36
	leftPanelWidthNarrow = 18
	// leftPanelBreakpointMid is where the group drops from double width to the
	// middle step, which is the narrowest terminal that can still afford it.
	leftPanelBreakpointMid = 76

	// Preferred interior widths of the utility views' leading columns.
	logListPaneWidthWide    = 46
	logListPaneWidthNarrow  = 32
	settingsPaneWidthWide   = 40
	settingsPaneWidthNarrow = 30

	// wideBreakpoint picks the roomier column hints.
	wideBreakpoint = 108
	// minUsableWidth and minUsableHeight are where the frame stops being able to
	// show three stacked panels with their borders intact.
	minUsableWidth = 38
	// Four stacked panels need four sets of borders plus one row each, so a frame
	// shorter than this cannot show the panel group intact.
	minUsableHeight = 22
	headerHeight    = 1
	footerHeight    = 1
)

// frameSize returns the area the panel frame occupies. The frame spans the full
// terminal width: the left column is part of it, not a sidebar beside it.
func (m *Model) frameSize() (width, height int) {
	return max(m.width, 1), max(m.height-headerHeight-footerHeight, 1)
}

// viewPaneWidth picks the roomier interior width on a wide terminal.
func (m *Model) viewPaneWidth(wide, narrow int) int {
	if m.width >= wideBreakpoint {
		return wide
	}
	return narrow
}

// fit truncates to w cells and pads with spaces, so columns always align. It is
// width-aware, which matters because room and seat labels are CJK.
func (m *Model) fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, m.glyphs.Ellipsis)
	}
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// clampInt constrains v to [low, high].
func clampInt(v, low, high int) int { return min(max(v, low), high) }

// clip truncates without padding.
func (m *Model) clip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		return ansi.Truncate(s, w, m.glyphs.Ellipsis)
	}
	return s
}

// toLines splits a pre-rendered block into exactly count lines, so a panel body
// always fills the interior the frame reserved for it.
func toLines(block string, count int) []string {
	if count <= 0 {
		return nil
	}
	src := strings.Split(strings.TrimRight(block, "\n"), "\n")
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		if i < len(src) {
			out = append(out, src[i])
			continue
		}
		out = append(out, "")
	}
	return out
}

// contextSummary is the middle segment of the header: what the user is currently
// working on, in the terms of the workflow rather than of the UI.
func (m *Model) contextSummary() string {
	switch m.overlay {
	case OverlayLogs:
		return fmt.Sprintf("拦截 %d 次", m.login.BlockedSeatRequests())
	case OverlayForm:
		if m.selection != nil {
			return m.selection.SeatNum + " 号 · 未提交"
		}
		return ""
	}
	var parts []string
	if m.rooms.day != "" {
		parts = append(parts, m.rooms.day)
	}
	if m.room.id != 0 {
		room := m.room.name
		if room == "" {
			room = "房间 " + itoa(m.room.id)
		}
		if slot, ok := m.room.currentSlot(); ok {
			room += " · " + slot.StartTime + "–" + slot.EndTime
		}
		parts = append(parts, room)
	}
	return strings.Join(parts, " · ")
}

// headerStatus is the right-aligned segment of the header. It always answers
// "am I logged in?", from whichever page the user is on.
func (m *Model) headerStatus() string {
	parts := []string{m.sessionSummary()}
	if m.session.home != nil {
		if clock := m.serverClock(); clock != "" {
			parts = append(parts, m.theme.MutedText.Render("服务器 "+clock))
		}
	} else if m.cfg.Proxy != "" {
		parts = append(parts, m.theme.FaintText.Render("代理 "+m.clip(m.cfg.Proxy, 24)))
	}
	if m.session.busy {
		parts = append([]string{m.theme.Accent.Render(m.spinner.View())}, parts...)
	}
	return strings.Join(parts, "  ")
}

// badge renders a status token such as "已登录" or "未登录".
func (m *Model) badge(text string, s severity) string {
	style := m.theme.severityStyle(s)
	marker := m.glyphs.Ring
	switch s {
	case sevOK:
		marker = m.glyphs.Dot
	case sevErr:
		marker = m.glyphs.Cross
	case sevWarn:
		marker = m.glyphs.Dot
	}
	return style.Render(marker + " " + text)
}

// stateView renders a centred empty / loading / error block. It is the single
// implementation behind every "nothing here", "working", and "something went
// wrong" screen, which keeps those states visually consistent.
//
// A panel can be one row tall -- the panel group gives four of them a share of a
// short terminal -- and a centred block that tall shows nothing but a blank (or a
// lone glyph), which reads as "there is nothing here" rather than "press enter".
// Below three rows the state therefore collapses into one line carrying the
// marker, the title and the action.
func (m *Model) stateView(marker string, s severity, title, detail, hint string, width, height int) string {
	style := m.theme.severityStyle(s)
	if height <= 2 && width > 0 {
		line := style.Render(marker)
		if title != "" {
			line += " " + m.theme.Heading.Render(title)
		}
		if hint != "" {
			line += m.theme.FaintText.Render(" · " + hint)
		}
		return m.clip(line, width)
	}
	lines := []string{style.Render(marker)}
	if title != "" {
		lines = append(lines, m.theme.Heading.Render(m.clip(title, width)))
	}
	if detail != "" {
		lines = append(lines, m.theme.MutedText.Render(m.clip(detail, width)))
	}
	if hint != "" {
		lines = append(lines, m.theme.FaintText.Render(m.clip(hint, width)))
	}
	block := lipgloss.JoinVertical(lipgloss.Center, lines...)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, block)
}

// loadingView renders the spinner with a message.
func (m *Model) loadingView(text string, width, height int) string {
	block := lipgloss.JoinHorizontal(lipgloss.Left,
		m.theme.Accent.Render(m.spinner.View()),
		" ",
		m.theme.MutedText.Render(text),
	)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, block)
}

// readOnlyView is the light state: something has not been chosen yet, so the
// panel has nothing dynamic to show. It is drawn with the faint style and the
// lightest glyphs on purpose -- a read-only panel should look read-only -- and it
// always names the key that would fill it.
func (m *Model) readOnlyView(title, hint string, width, height int) string {
	lines := []string{m.theme.FaintText.Render(m.clip(m.glyphs.Ring+" 只读", width))}
	if title != "" {
		lines = append(lines, m.theme.FaintText.Render(m.clip(title, width)))
	}
	for _, wrapped := range wrapText(hint, max(width, 8)) {
		lines = append(lines, m.theme.FaintText.Render(wrapped))
	}
	for i := range lines {
		lines[i] = m.fit(lines[i], width)
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

// emptyView renders "no data" with a next step.
func (m *Model) emptyView(title, hint string, width, height int) string {
	return m.stateView(m.glyphs.Ring, sevNone, title, "", hint, width, height)
}

// errorView renders a classified failure. Only the short message is shown here;
// the full detail lives on the diagnostics screen.
func (m *Model) errorView(appErr *AppError, width, height int) string {
	if appErr == nil {
		return ""
	}
	hint := appErr.Hint
	if hint == "" && appErr.Recoverable() {
		hint = "按 r 重试。"
	}
	return m.stateView(m.glyphs.Cross, appErr.severity(), appErr.Short, "", hint, width, height)
}

// headerView renders one line of context: what the application is, which view is
// on screen, what the user is working on, and the remote state.
func (m *Model) headerView() string {
	sep := m.theme.FaintText.Render("  " + m.glyphs.Bullet + "  ")
	base := m.theme.AppName.Render("WFU 座位预约") + sep + m.theme.Heading.Render(m.viewTitle())
	right := m.headerStatus()

	inner := max(m.width-2, 1)
	// The left segment owns everything the right segment does not, minus one cell
	// of breathing room so the two never touch.
	leftRoom := max(inner-lipgloss.Width(right), 1)
	contextRoom := leftRoom - lipgloss.Width(base) - lipgloss.Width(sep) - 1
	context := ""
	if summary := m.contextSummary(); summary != "" && contextRoom > 6 {
		context = sep + m.theme.MutedText.Render(m.clip(summary, contextRoom))
	}
	return m.theme.Header.Render(m.fit(base+context, leftRoom) + right)
}

// sessionSummary describes the login state in one token.
func (m *Model) sessionSummary() string {
	switch {
	case m.session.phase == phaseConfirmed:
		return m.badge("已登录", sevOK)
	case m.session.phase == phaseWaiting || m.session.phase == phaseStarting:
		return m.badge("等待扫码", sevWarn)
	case m.session.phase == phaseExpired:
		return m.badge("二维码已过期", sevWarn)
	case m.session.phase == phaseFailed:
		return m.badge("登录失败", sevErr)
	default:
		return m.badge("未登录", sevNone)
	}
}

// serverClock renders the synchronised server time.
func (m *Model) serverClock() string {
	if m.client == nil {
		return ""
	}
	now, err := m.client.ServerNow()
	if err != nil {
		return ""
	}
	return now.Format("2006-01-02 15:04:05")
}

// footerView renders the context help bar: the keys that matter for the focused
// panel, added while they fit so the bar always tells the user what to press next
// without ever becoming a wall of keys.
func (m *Model) footerView() string {
	if len(m.toasts) > 0 {
		return m.toastLine()
	}
	primary, always := m.footerBindings()
	sep := m.theme.FaintText.Render("  " + m.glyphs.Bullet + "  ")
	limit := max(m.width-2, 8)

	alwaysText := renderHints(m, sep, always)
	line := alwaysText
	for _, b := range primary {
		candidate := renderHints(m, sep, []key.Binding{b})
		joined := candidate + sep + alwaysText
		if lipgloss.Width(joined) > limit {
			break
		}
		if line == alwaysText {
			line = joined
			continue
		}
		line = line[:len(line)-len(alwaysText)-len(sep)] + sep + candidate + sep + alwaysText
	}
	if lipgloss.Width(line) > limit {
		line = alwaysText
	}
	return m.theme.Footer.Render(m.fit(line, limit))
}

// renderHints joins the "key  description" fragments of a binding set.
func renderHints(m *Model, sep string, bindings []key.Binding) string {
	parts := make([]string, 0, len(bindings))
	for _, b := range bindings {
		parts = append(parts, m.theme.Label.Render(hint(b)))
	}
	return strings.Join(parts, sep)
}

// footerBindings lists the shortcut hints for the focused panel, most important
// first and capped so the bar never becomes a wall of keys.
func (m *Model) footerBindings() (primary, always []key.Binding) {
	var hints []key.Binding
	hints = append(hints, m.keys.HintPanels)

	switch m.overlay {
	case OverlayLogs:
		hints = append(hints, m.keys.HintMove, m.keys.HintScroll)
	case OverlayForm:
		hints = append(hints, m.keys.HintScroll)
	default:
		switch {
		case m.zone == ZoneMain && (m.side == PanePeriods || m.side == PaneSeats):
			// The content of these panels is a picker: both keys select, and esc
			// is the way back to the panel group.
			hints = append(hints, m.keys.HintMove, m.keys.Space, m.keys.Back)
		case m.zone != ZoneSide:
			hints = append(hints, m.keys.Back)
			if m.zone == ZonePreview && m.selection != nil {
				hints = append(hints, m.keys.HintOpen, m.keys.Auto)
			}
		case m.side == PaneRooms:
			hints = append(hints, m.keys.HintMove, m.keys.HintOpen, m.keys.Filter)
		case m.side == PanePeriods:
			hints = append(hints, m.keys.HintMove, m.keys.HintOpen, m.keys.Scan)
		case m.side == PaneSeats && m.session.phase != phaseConfirmed:
			hints = append(hints, m.keys.Enter, m.keys.Refresh)
		default:
			// Selecting is space, entering the content is enter: the bar says both,
			// because they no longer mean the same thing.
			hints = append(hints, m.keys.HintMove, m.keys.HintSelect, m.keys.HintOpen, m.keys.Auto)
		}
		// The utility views are global, so they are offered wherever there is
		// room left on the line.
		hints = append(hints, m.keys.Logs)
	}

	const maxHints = 5
	if len(hints) > maxHints {
		hints = hints[:maxHints]
	}
	always = []key.Binding{m.keys.Help, m.keys.Quit}
	if m.db != nil {
		always = append([]key.Binding{displayHint("u", "账号"), displayHint("a", "创建预订")}, always...)
	}
	return hints, always
}

// toastLine renders the most recent notification in place of the hint line, so
// feedback appears exactly where the user is already looking.
func (m *Model) toastLine() string {
	if len(m.toasts) == 0 {
		return ""
	}
	t := m.toasts[len(m.toasts)-1]
	style := m.theme.severityStyle(t.sev)
	marker := m.glyphs.Arrow
	if t.sev == sevErr {
		marker = m.glyphs.Cross
	} else if t.sev == sevOK {
		marker = m.glyphs.Check
	}
	text := style.Render(marker + " " + t.text)
	return m.theme.Footer.Render(m.fit(text, max(m.width-2, 1)))
}

// pushToast queues a transient notification.
func (m *Model) pushToast(s severity, format string, args ...any) {
	m.toasts = append(m.toasts, toast{
		text:    fmt.Sprintf(format, args...),
		sev:     s,
		expires: time.Now().Add(toastTTL),
	})
	if len(m.toasts) > maxToasts {
		m.toasts = m.toasts[len(m.toasts)-maxToasts:]
	}
}

// expireToasts drops notifications whose time is up.
func (m *Model) expireToasts(at time.Time) {
	if len(m.toasts) == 0 {
		return
	}
	kept := m.toasts[:0]
	for _, t := range m.toasts {
		if t.expires.After(at) {
			kept = append(kept, t)
		}
	}
	m.toasts = kept
}

// constrain enforces the frame invariant. JoinVertical pads every line to the
// widest line, so a single over-wide string would widen the whole frame; and a
// content block taller than the content area would push the footer off screen.
// Clipping once here makes both impossible regardless of per-screen maths.
func (m *Model) constrain(content string, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		if lipgloss.Width(line) > width {
			lines[i] = ansi.Truncate(line, width, "")
		}
	}
	return strings.Join(lines, "\n")
}
