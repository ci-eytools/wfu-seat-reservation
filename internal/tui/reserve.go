package tui

import (
	"context"
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"wfuseat/internal/chaoxing"
	"wfuseat/internal/config"
)

// Reservation
//
// Writing is the one thing this client does that changes the remote service, so
// it is built as an explicit, multi-step action:
//
//	the user picks a seat  ->  the details card shows the signed preview
//	enter on the details    ->  the full local encoding
//	enter on the form       ->  a confirmation printing method, path and body
//	confirm                 ->  exactly one request, to exactly that target
//
// Nothing is guessed and nothing is silent: when the seat page does not name a
// target, the action refuses and says so rather than inventing an endpoint.

// reserveConfig maps the user's settings onto the client.
func reserveConfig(cfg config.Config) chaoxing.ReserveConfig {
	return chaoxing.ReserveConfig{
		SubmitURL: cfg.SubmitURL,
		CancelURL: cfg.CancelURL,
		Method:    cfg.SubmitMethod,
	}
}

// beginSubmit prepares the confirmation for a reservation.
func (m *Model) beginSubmit() (tea.Model, tea.Cmd) {
	if m.write.busy {
		m.pushToast(sevInfo, "上一个请求还在进行")
		return m, nil
	}
	if m.selection == nil {
		m.pushToast(sevInfo, "还没有选座预览")
		return m, nil
	}
	if !m.cfg.AllowSubmit {
		m.write.err = &AppError{
			Kind:  kindUnsupported,
			Short: "此账号已暂停预订（账号面板 p 开启）",
			Hint:  "按 , 打开设置，把「允许提交」改为开。",
		}
		m.pushToast(sevWarn, "此账号已暂停预订（账号面板 p 开启）")
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
		m.pushToast(sevErr, "无法提交：选座页没有提供提交地址")
		return m, nil
	}
	m.pushToast(sevInfo, "请确认下面的请求")
	m.confirm = confirmState{
		prompt:  "提交预约 " + m.selection.SeatNum + " 号？",
		detail:  "这是真实写请求：服务端会占用该座位。",
		request: requestBlock(targets.Submit, m.selection.PreparedForm()),
		confirm: "提交预约",
		action:  confirmReserve,
		// Safe by default: a stray enter cancels rather than reserves.
		focusYes: false,
	}
	m.overlay = OverlayConfirm
	return m, nil
}

// beginQueue prepares the confirmation for a waitlist request. It is the way in
// when a room has no bookable period left.

// requestBlock renders the exact request for the confirmation: method, path, the
// page (or setting) that named it, and the body that will be sent. The signature
// is included, because that is what the service checks.
func requestBlock(target chaoxing.WriteTarget, body string) string {
	method := target.Method
	if method == "" {
		method = "POST"
	}
	lines := []string{
		"请求  " + method + " " + target.Path(),
		"来源  " + target.Source,
	}
	if target.Note != "" {
		lines = append(lines, "依据  "+target.Note)
	}
	lines = append(lines, "参数  "+body)
	return strings.Join(lines, "\n")
}

// beginWrite starts the request the user just confirmed.
func (m *Model) beginWrite(action confirmAction) tea.Cmd {
	if m.write.busy {
		return nil
	}
	m.genWrite++
	m.write.busy = true
	m.write.result = nil
	m.write.err = nil
	switch action {
	default:
		m.write.what = "预约"
		return m.cmdSubmitReservation()
	}
}

// onReserveResult records the service's verdict. Success is only claimed when the
// service said so; anything else is reported as an unconfirmed attempt.
func (m *Model) onReserveResult(msg reserveResultMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.genWrite {
		return m, nil
	}
	m.write.busy = false
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return m, nil
		}
		m.write.err = classify(msg.err)
		m.write.result = nil
		m.pushToast(sevErr, "%s", m.write.err.Short)
		m.layout()
		return m, nil
	}
	m.write.err = nil
	m.write.result = msg.result
	if msg.result.OK {
		if msg.action == confirmReserve && m.selection != nil {
			m.selection.Submitted = true
			m.selection.Result = msg.result.Message
		}
		m.pushToast(sevOK, "服务端已确认：%s", msg.result.Message)
	} else {
		m.pushToast(sevWarn, "服务端未确认：%s", msg.result.Message)
	}
	m.layout()
	return m, nil
}

// writeBlock is the details card's report of the last attempt.
func (m *Model) writeBlock() string {
	if m.write.err != nil {
		return m.write.err.Short
	}
	if m.write.result != nil {
		state := "未确认"
		if m.write.result.OK {
			state = "已确认"
		}
		return state + " · " + m.write.result.Message
	}
	return ""
}
