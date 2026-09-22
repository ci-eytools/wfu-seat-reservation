package tui

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"time"
	"wfuseat/internal/config"
	"wfuseat/internal/storage"
)

func (m *Model) accountLabel() string {
	if m.remote != nil {
		return m.remoteAccountLabel()
	}
	if m.cfg.Account != "" && !storage.IsIdentifiedAccount(m.cfg.Account) && !strings.HasPrefix(m.cfg.Account, "pending-") {
		return "凭证待验证"
	}
	if strings.HasPrefix(m.cfg.Account, "pending-") {
		return "添加账号"
	}
	return m.cfg.Account
}
func (m *Model) accountIDs() []string {
	if m.remote != nil {
		return m.remoteIDs()
	}
	ids, _ := storage.Accounts(m.cfg.Root)
	var saved []string
	for _, id := range ids {
		if !strings.HasPrefix(id, "pending-") {
			saved = append(saved, id)
		}
	}
	return saved
}
func (m *Model) accountRows(width, height int, focused bool) []string {
	ids := m.accountIDs()
	m.accountList.SetCount(len(ids))
	m.accountList.SetHeight(height)
	first, last := m.accountList.Window()
	var lines []string
	for i := first; i < last; i++ {
		name := ids[i]
		if !storage.IsIdentifiedAccount(name) && !strings.HasPrefix(name, "pending-") {
			name = "凭证待验证"
		}
		if strings.HasPrefix(name, "pending-") {
			name = "待扫码"
		}
		if m.remote != nil {
			name = m.remote.Profiles()[ids[i]]
		}
		mark := " "
		if ids[i] == m.cfg.Account {
			mark = "*"
		}
		lines = append(lines, m.listRow(width, i == m.accountList.cursor, focused, m.plainCell(mark+" "+name, width-2)))
	}
	return padList(lines, height)
}
func (m *Model) accountControl(msg tea.KeyMsg) (bool, tea.Model, tea.Cmd) {
	if m.db == nil {
		return false, m, nil
	}
	key := msg.String()
	if key == "n" {
		if m.busyClient() {
			m.pushToast(sevWarn, "等待当前操作完成")
			return true, m, nil
		}
		if strings.HasPrefix(m.cfg.Account, "pending-") {
			m.focusSide(PaneStatus)
			m.zone = ZoneMain
			return true, m, m.beginStartQR()
		}
		cfg, path, err := config.LoadAccount(m.cfg.Root, fmt.Sprintf("pending-%d", time.Now().UnixNano()))
		if err != nil {
			m.pushToast(sevErr, "无法准备登录：%s", err)
			return true, m, nil
		}
		cfg.Proxy = m.cfg.Proxy
		if _, err = config.Save(cfg); err != nil {
			m.pushToast(sevErr, "无法准备登录：%s", err)
			return true, m, nil
		}
		var options []Option
		if m.remote != nil {
			options = append(options, WithRemote(m.remote.Fresh()))
		}
		next, err := New(cfg, path, options...)
		if err != nil {
			m.pushToast(sevErr, "无法准备登录：%s", err)
			return true, m, nil
		}
		next.addAccountReturn = m
		next.genSession = m.genSession + 1000
		next.width, next.height, next.ready = m.width, m.height, m.ready
		next.focusSide(PaneStatus)
		next.zone = ZoneMain
		next.layout()
		return true, next, tea.Batch(next.Init(), next.beginStartQR())
	}
	if key == "p" && m.zone == ZoneMain {
		if m.remote != nil {
			cfg := m.cfg
			cfg.AllowSubmit = !cfg.AllowSubmit
			return true, m, m.cmdSaveConfig(cfg)
		}
		if m.db == nil {
			return true, m, nil
		}
		enabled := !m.cfg.AllowSubmit
		if e := m.db.SetEnabled(enabled); e != nil {
			m.pushToast(sevErr, "开关保存失败：%s", e)
			return true, m, nil
		}
		m.cfg.AllowSubmit = enabled
		m.settings.draft = m.cfg
		return true, m, m.cmdSaveConfig(m.cfg)
	}
	if key == "D" && m.zone == ZoneMain {
		if m.busyClient() {
			m.pushToast(sevWarn, "等待当前操作完成")
			return true, m, nil
		}
		m.confirm = confirmState{prompt: "删除此账号？", detail: "删除本机保存的登录凭证、配置和预订记录；不会撤销服务器上的已有预约。", request: m.accountLabel(), confirm: "删除", action: confirmDeleteAccount}
		if m.remote != nil {
			m.confirm.detail = "删除此后端保存的学校凭证、配置与任务，并使所有客户端会话失效；不会撤销学校已有预约。"
		}
		m.overlay = OverlayConfirm
		return true, m, nil
	}
	if m.zone == ZoneSide {
		switch key {
		case "j", "down":
			m.accountList.Move(1)
			return true, m, nil
		case "k", "up":
			m.accountList.Move(-1)
			return true, m, nil
		case "enter":
			ids := m.accountIDs()
			i, ok := m.accountList.Selected()
			if ok && i < len(ids) && ids[i] != m.cfg.Account {
				next, cmd := m.switchAccount(ids[i])
				return true, next, cmd
			}
			m.zone = ZoneMain
			if m.session.phase != phaseConfirmed {
				return true, m, m.beginStartQR()
			}
			return true, m, nil
		}
	}
	return false, m, nil
}
func (m *Model) deleteAccount() (tea.Model, tea.Cmd) {
	if m.remote != nil {
		return m, m.remoteDelete()
	}
	if e := storage.RemoveAccount(m.cfg.Root, m.cfg.Account); e != nil {
		m.pushToast(sevErr, "删除失败：%s", e)
		return m, nil
	}
	ids := m.accountIDs()
	id := fmt.Sprintf("pending-%d", time.Now().UnixNano())
	if len(ids) > 0 {
		id = ids[0]
	}
	return m.switchAccount(id)
}
