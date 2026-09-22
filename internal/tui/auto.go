package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"wfuseat/internal/chaoxing"
)

// Scheduled reservation
//
// A reservation window opens at a fixed moment, and the useful thing a client can
// do is have the request ready before that moment and fire it at it. This is that
// machinery, with one deliberate limit: the automatic shot is a SIMULATION. It
// builds exactly the request that would be sent, prints it, and records it in the
// redacted log -- but it never reaches the network. Sending for real stays a
// manual action, behind the confirmation, because an unattended write to somebody
// else's scheduling system is not something a tool should do by default.
//
// The only automatic network traffic is the same read-only query a manual press
// would make, and only if the request had not been prepared yet.

// autoState is an armed scheduled request.
type autoState struct {
	armed bool
	// at is the local moment the request fires.
	at time.Time
	// reason explains where that moment came from, for the confirmation.
	reason string
	// request is the exact body that will be reported when it fires.
	request string
	target  chaoxing.WriteTarget
	// shots counts how many times the schedule has fired, and result keeps the
	// last report so the preview card can show it after the fact.
	shots  int
	result string
	// fired is set once the scheduled moment has passed.
	fired bool
}

// autoTarget is the moment an armed request fires: the server's own reservation
// opening time when it named one, otherwise now plus the configured lead.
func (m *Model) autoTarget() (time.Time, string) {
	if _, opensAt, ok := m.client.ReserveWindow(); ok && !opensAt.IsZero() && opensAt.After(time.Now()) {
		return opensAt, "预约窗口开放时刻（服务器提供）"
	}
	lead := time.Duration(m.cfg.AutoLeadSeconds) * time.Second
	return time.Now().Add(lead), fmt.Sprintf("现在 + %d 秒（设置中的提前量）", m.cfg.AutoLeadSeconds)
}

// beginAutoReserve prepares the confirmation for a scheduled request.
func (m *Model) beginAutoReserve() (tea.Model, tea.Cmd) {
	if m.db != nil {
		return m.beginScheduledJob()
	}
	if m.write.busy {
		m.pushToast(sevInfo, "上一个请求还在进行")
		return m, nil
	}
	if m.selection == nil {
		m.pushToast(sevInfo, "先按 space 选中座位，再按 a 定时")
		return m, nil
	}
	targets := m.client.ReserveTargets()
	if !targets.HasSubmit {
		m.write.err = &AppError{
			Kind:   kindUnsupported,
			Short:  "选座页没有提供提交地址",
			Detail: "仓库里没有记录该接口，本工具不会猜测地址。",
			Hint:   "重新打开房间获取服务端预约入口。",
		}
		m.pushToast(sevErr, "无法定时：选座页没有提供提交地址")
		return m, nil
	}
	at, reason := m.autoTarget()
	seat := m.selection.SeatNum
	m.confirm = confirmState{
		prompt: "定时模拟预约 " + seat + " 号（不会真实发送）？",
		detail: "到点自动生成并记录这次请求，只写审计日志，不发网络请求。",
		request: requestBlock(targets.Submit, m.selection.PreparedForm()) +
			"\n触发  " + at.Format("2006-01-02 15:04:05") + "（" + shortDuration(time.Until(at)) + " 后）" +
			"\n时刻  " + reason,
		confirm:  "定时（模拟）",
		action:   confirmAutoReserve,
		focusYes: false,
	}
	m.overlay = OverlayConfirm
	return m, nil
}

// armAutoReserve records the schedule once the user confirms it.
func (m *Model) armAutoReserve() tea.Cmd {
	at, reason := m.autoTarget()
	target := m.client.ReserveTargets().Submit
	m.auto = autoState{
		armed:   true,
		at:      at,
		reason:  reason,
		request: m.selection.PreparedForm(),
		target:  target,
	}
	m.pushToast(sevInfo, "已定时 %s（模拟，不发送真实请求）", at.Format("15:04:05"))
	m.layout()
	return nil
}

// cancelAutoReserve forgets an armed schedule.
func (m *Model) cancelAutoReserve() bool {
	if !m.auto.armed {
		return false
	}
	m.auto = autoState{result: "已取消定时预约"}
	m.pushToast(sevInfo, "已取消定时预约")
	m.layout()
	return true
}

// dueAutoReserve reports that the armed moment has arrived. It stops the schedule
// so a shot is never taken twice.
func (m *Model) dueAutoReserve(now time.Time) (autoState, bool) {
	if !m.auto.armed || now.Before(m.auto.at) {
		return autoState{}, false
	}
	shot := m.auto
	m.auto.armed = false
	return shot, true
}

// fireAutoReserve performs the scheduled shot. It is a simulation by
// construction: the request is rendered and logged, and nothing is sent.
func (m *Model) fireAutoReserve(shot autoState) tea.Cmd {
	m.auto.shots++
	m.auto.fired = true
	m.auto.result = fmt.Sprintf("已模拟第 %d 次：%s %s",
		m.auto.shots, shot.target.Method, shot.target.Path())
	m.pushToast(sevOK, "定时预约已模拟（未真实发送）：%s 号", seatOf(shot.request))
	m.layout()
	return m.cmdLogSimulation(shot)
}

// cmdLogSimulation writes the audit record for a simulated request.
func (m *Model) cmdLogSimulation(shot autoState) tea.Cmd {
	if m.login == nil || m.login.Session == nil {
		return nil
	}
	log := m.login.Session.Log
	if log == nil {
		return nil
	}
	fields := map[string]any{
		"method":    shot.target.Method,
		"path":      shot.target.Path(),
		"source":    shot.target.Source,
		"seat":      seatOf(shot.request),
		"body":      shot.request,
		"at_local":  shot.at.Format(time.RFC3339),
		"shots":     m.auto.shots,
		"simulated": true,
	}
	return func() tea.Msg {
		log.Event("simulated_reserve", fields)
		return nil
	}
}

// autoCountdown is the preview card's line for an armed schedule.
func (m *Model) autoCountdown() string {
	if m.auto.armed {
		return "定时 " + m.auto.at.Format("15:04:05") + " · " + shortDuration(time.Until(m.auto.at)) + " 后模拟"
	}
	return m.auto.result
}

// shortDuration renders a countdown without the sub-second noise time.Duration
// prints by default.
func shortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%d小时%02d分", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%d分%02d秒", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%d秒", int(d.Seconds()))
	}
}

// seatOf pulls the seat number out of an encoded query string, for messages.
func seatOf(body string) string {
	for _, pair := range splitAmp(body) {
		if len(pair) > 8 && pair[:8] == "seatNum=" {
			if value := pair[8:]; value != "" {
				return value
			}
		}
	}
	return "—"
}

func splitAmp(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '&' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
