package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"wfuseat/internal/chaoxing"
)

// statusContentPanel is the content of the project panel: what this tool is, and
// the QR login that every session starts with. It is also the content whenever no
// session exists, so the frame keeps its shape and the keyboard never has to jump
// when the login completes.
func (m *Model) statusContentPanel() panelSlot {
	return panelSlot{
		owner:  panelOwner{zone: ZoneMain, side: PaneStatus},
		title:  "账号详情",
		trail:  m.sessionPhaseLabel,
		status: m.statusContentStatusLine,
		body:   m.statusContentBody,
	}
}

func (m *Model) sessionPhaseLabel() string {
	switch m.session.phase {
	case phaseStarting:
		return "正在获取二维码"
	case phaseWaiting:
		return "等待扫码"
	case phaseCompleting:
		return "正在完成登录"
	case phaseConfirmed:
		return "已登录"
	case phaseExpired:
		return "二维码已过期"
	case phaseFailed:
		return "失败"
	default:
		return "未登录"
	}
}

// statusContentStatusLine packs the session facts into the panel's status line,
// which keeps the body free for the code the user actually needs to see.
func (m *Model) statusContentStatusLine() string {
	if m.session.phase == phaseConfirmed {
		return "j/k 选择 · enter 编辑 · space 开关 · ctrl+s 保存 · esc 返回"
	}
	proxy := m.cfg.Proxy
	if proxy == "" {
		proxy = "直连"
	}
	return fmt.Sprintf("代理 %s · 服务器日期 %s · 拦截 %d 次",
		proxy, m.dayOrDash(), m.login.BlockedSeatRequests())
}

// statusContentBody renders the live part of the project panel: the QR code while
// a login is in progress, and the project's own description otherwise.
func (m *Model) statusContentBody(width, height int, focused bool) []string {
	if m.session.phase == phaseConfirmed {
		rows := min(height, len(settingFields))
		m.settings.list.SetHeight(rows)
		lines := []string{m.theme.Heading.Render("账号与凭证")}
		lines = append(lines, strings.Split(m.projectView(width, 8, ""), "\n")...)
		lines = append(lines, "", m.theme.Heading.Render("账号配置"))
		lines = append(lines, m.settingsBodyLines(width, rows, focused)...)
		if i := m.settings.list.cursor; i >= 0 && i < len(settingFields) {
			lines = append(lines, "")
			help := settingFields[i].Help
			if m.remote != nil && i == 0 {
				help = "远程凭证和任务保存在此服务端；关闭 TUI 后仍由服务端执行。--server=local 返回本地。"
			}
			for _, line := range wrapText(help, width) {
				lines = append(lines, m.theme.MutedText.Render(line))
			}
		}
		if m.settings.err != nil {
			lines = append(lines, m.theme.BadgeErr.Render(m.clip(m.settings.err.Short, width)))
		} else if m.settings.dirty {
			lines = append(lines, m.theme.BadgeWarn.Render("有未保存的修改 · ctrl+s 保存"))
		} else if m.settings.status != "" {
			lines = append(lines, m.theme.BadgeOK.Render("已保存"))
		}
		return padList(lines, height)
	}
	switch {
	case m.session.err != nil:
		return toLines(m.errorView(m.session.err, width, height), height)

	case m.session.busy && len(m.session.qrGrid) == 0:
		text := "正在获取二维码…"
		if m.session.phase == phaseCompleting {
			text = "已扫码，正在完成登录并验证座位页…"
		}
		return toLines(m.loadingView(text, width, height), height)

	case len(m.session.qrGrid) > 0:
		return toLines(m.sessionQRView(width, height), height)

	case m.session.phase == phaseConfirmed:
		return toLines(m.projectView(width, height, m.loggedInHint()), height)

	case m.session.phase == phaseExpired:
		return toLines(m.stateView(m.glyphs.Ring, sevWarn, "二维码已过期", "",
			"按 enter 重新生成二维码。", width, height), height)

	default:
		return toLines(m.stateView(m.glyphs.Ring, sevNone, "尚未登录",
			"登录后才能查询房间、时段与空闲座位。",
			"按 enter 生成二维码，然后用智慧潍苑 APP 扫码并确认。", width, height), height)
	}
}

func (m *Model) loggedInHint() string {
	hint := "在 " + paneTitle(PaneRooms) + " 选择房间。"
	if m.session.callback != nil {
		hint = fmt.Sprintf("网页登录状态 %s · 在 %s 选择房间。",
			stateLabel(chaoxingState(m.session.callback)), paneTitle(PaneRooms))
	}
	return hint
}

// projectView is the project panel's content once the screen is not needed for a
// code: what the tool is, what it will and will not do, and the session's facts.
func (m *Model) projectView(width, height int, hint string) string {
	enabled := "关闭"
	if m.cfg.AllowSubmit {
		enabled = "开启"
	}
	worker := "等待计划"
	if m.db != nil {
		if b, e := m.db.Get("worker_error"); e == nil && len(b) > 0 {
			worker = string(b)
		}
	}
	return strings.Join(m.detailRows(width, height, [][2]string{{"账号", m.accountLabel()}, {"凭证", m.sessionPhaseLabel()}, {"自动预订", enabled}, {"调度状态", worker}, {"开放时间", "按教室和预约日期动态获取"}, {"代理", m.cfg.Proxy}, {"固定延迟", fmt.Sprintf("%d ms", m.cfg.DelayMS)}, {"操作", "n 新增 · p 开关 · D 删除"}}), "\n")
}

func (m *Model) sessionQRView(width, height int) string {
	if m.qrWindowCancel != nil {
		return m.stateView(m.glyphs.Ring, sevInfo, "二维码已在独立全屏窗口展示", "扫码确认后自动关闭。", "Esc 取消本次登录。", width, height)
	}
	if m.qrWindowErr != nil {
		return m.stateView(m.glyphs.Ring, sevWarn, "无法打开二维码窗口", m.qrWindowErr.Error(), "放大终端后按 Enter 重新生成二维码。", width, height)
	}

	grid := m.session.qrGrid
	modules := len(grid)
	qw, qh := qrRenderSize(modules, qrQuietZone, m.cfg.ASCIIOnly)
	// The rendered block is the code plus a blank line and the hint beneath it.
	if qw > width || qh+2 > height {
		return m.stateView(m.glyphs.Ring, sevWarn,
			"终端尺寸不足以显示二维码",
			fmt.Sprintf("需要约 %d×%d 字符，当前可用 %d×%d。", qw, qh+2, width, height),
			"放大终端窗口后按 enter 重新生成；也可继续用 notebook 扫码。", width, height)
	}

	remaining := int(time.Until(m.session.deadline).Seconds())
	if remaining < 0 {
		remaining = 0
	}
	hint := fmt.Sprintf("剩余 %ds · 已轮询 %d 次 · 用智慧潍苑 APP 扫码并确认",
		remaining, m.session.polls)

	content := strings.TrimRight(renderQRText(grid, qrQuietZone, m.cfg.ASCIIOnly), "\n") +
		"\n\n" + m.theme.MutedText.Render(hint)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, content)
}

// keySession handles the project panel and its login content. enter is the only
// action: it starts a code, or moves on once a session exists.
func (m *Model) keySession(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if !key.Matches(msg, m.keys.Enter) {
		return m, nil
	}
	if m.session.phase == phaseConfirmed {
		m.focusSide(PaneRooms)
		return m, nil
	}
	// The code is rendered in the content beside the panel, so the keyboard goes
	// there to show it.
	m.focusZone(ZoneMain)
	return m, m.beginStartQR()
}

// stateLabel renders a callback state token for people rather than for logs.
func stateLabel(state string) string {
	switch state {
	case "web_login_confirmed":
		return "已确认"
	case "web_login_rejected":
		return "被拒绝"
	case "web_login_http_error":
		return "HTTP 错误"
	case "web_login_non_json":
		return "响应不是 JSON"
	case "web_login_attempted_no_result":
		return "未返回结果"
	case "callback_http_error":
		return "回调 HTTP 错误"
	case "callback_loaded":
		return "已加载回调页"
	case "inspect_redirect":
		return "跳出允许域名"
	}
	return state
}

func chaoxingState(result map[string]any) string { return chaoxing.StateString(result) }
