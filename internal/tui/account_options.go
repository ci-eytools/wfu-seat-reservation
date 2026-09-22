package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"wfuseat/internal/chaoxing"
	"wfuseat/internal/config"
)

// OfficeHostName is the only host a configured write target may point at.
const OfficeHostName = chaoxing.OfficeHost

// settingKind distinguishes free-text fields from checkboxes. Toggles are what
// space acts on, which keeps space from doubling as a confirm key.
type settingKind int

const (
	settingText settingKind = iota
	settingToggle
)

// settingField describes one editable setting. Fields are addressed by index so
// the getter and setter stay in sync with the labels.
type settingField struct {
	Label string
	Help  string
	Kind  settingKind
}

var settingFields = [...]settingField{
	{Label: "代理", Help: "留空直连；保存后下次打开账号生效。"},
	{Label: "预订开关", Help: "关闭暂停此账号所有预订，开启后继续等待未来的开放窗口。", Kind: settingToggle},
	{Label: "固定延迟 ms", Help: "每次任务从 0 到此上限固定延迟（含端点）；0 不延迟。范围 0–60000 ms，后续候选不重复等待。"},
}

func newSettingsState(cfg config.Config) settingsState {
	input := textInput()
	s := settingsState{draft: cfg, input: input}
	s.list.SetCount(len(settingFields))
	s.list.SetHeight(len(settingFields))
	return s
}

// settingsColumns is the settings view: the fields beside an explanation of the
// focused one, which is the list/detail pair every other view uses.

func (m *Model) settingsTrailing() string {
	if m.settings.dirty {
		return "有未保存的修改"
	}
	return "已保存"
}

func (m *Model) settingsStatusLine() string {
	if m.settings.err != nil {
		return "错误：" + m.settings.err.Short
	}
	position, total := m.settings.list.Scrolled()
	return m.listPosition(position, total) + " · " + itoa(len(settingFields)) + " 项设置"
}

func (m *Model) settingsBodyLines(width, height int, focused bool) []string {
	const labelWidth = 13
	first, last := m.settings.list.Window()
	lines := make([]string, 0, height)
	for i := first; i < last; i++ {
		field := settingFields[i]
		if m.remote != nil && i == 0 {
			field.Label = "服务端"
		}
		selected := i == m.settings.list.cursor

		label := m.column(field.Label, labelWidth)
		var value listCell
		switch {
		case selected && m.settings.editing:
			value = listCell{
				text:  m.column(m.settings.input.View(), max(width-2-labelWidth, 8)),
				style: m.theme.RowNormal,
			}
		case field.Kind == settingToggle:
			// A checkbox reads as a value, not as a key hint.
			if m.settings.value(i) == "开" {
				value = listCell{text: m.column("[x] 开", max(width-2-labelWidth, 8)), style: m.theme.BadgeOK}
			} else {
				value = listCell{text: m.column("[ ] 关", max(width-2-labelWidth, 8)), style: m.theme.BadgeMuted}
			}
		default:
			raw := m.settings.value(i)
			if m.remote != nil && i == 0 {
				raw = m.remote.BaseURL
			}
			if raw == "" {
				value = listCell{text: m.column("（空）", max(width-2-labelWidth, 8)), style: m.theme.BadgeMuted}
			} else {
				value = m.plainCell(raw, max(width-2-labelWidth, 8))
			}
		}
		lines = append(lines, m.listRow(width, selected, focused, m.plainCell(label, labelWidth), value))
	}
	return padList(lines, height)
}

// settingsHelpBody shows what the focused setting means, where it is stored, and
// whether anything is pending. It is the detail side of the list/detail pair.
func (m *Model) settingsHelpBody(width, height int, focused bool) []string {
	lines := make([]string, 0, height)
	if index := m.settings.list.cursor; index >= 0 && index < len(settingFields) {
		lines = append(lines, m.theme.Heading.Render(m.clip(settingFields[index].Label, width)))
		lines = append(lines, "")
		for _, wrapped := range wrapText(settingFields[index].Help, width) {
			lines = append(lines, m.theme.MutedText.Render(wrapped))
		}
	}
	lines = append(lines, "")
	lines = append(lines, m.theme.Label.Render("配置文件"))
	for _, wrapped := range wrapText(m.cfgPath, width) {
		lines = append(lines, m.theme.FaintText.Render(wrapped))
	}
	if m.settings.dirty {
		lines = append(lines, "")
		lines = append(lines, m.badge("有未保存的修改", sevWarn))
	} else if m.settings.status != "" {
		lines = append(lines, "")
		lines = append(lines, m.theme.BadgeOK.Render(m.clip(m.settings.status, width)))
	}
	return padList(lines, height)
}

// settingsCursorIsToggle reports whether the focused field is a checkbox, which is
// what makes space meaningful.
func (m *Model) settingsCursorIsToggle() bool {
	index := m.settings.list.cursor
	return index >= 0 && index < len(settingFields) && settingFields[index].Kind == settingToggle
}

// toggleSetting flips the focused checkbox through the same validation path as a
// typed edit.
func (m *Model) toggleSetting() {
	if !m.settingsCursorIsToggle() {
		return
	}
	index := m.settings.list.cursor
	next := "开"
	if m.settings.value(index) == "开" {
		next = "关"
	}
	if err := m.settings.apply(index, next); err != nil {
		m.settings.err = &AppError{Kind: kindInput, Short: err.Error()}
		return
	}
	m.settings.dirty = true
	m.settings.err = nil
	m.settings.status = ""
}

// value renders the current draft value of a field.
func (s *settingsState) value(index int) string {
	switch index {
	case 0:
		return s.draft.Proxy
	case 1:
		if s.draft.AllowSubmit {
			return "开"
		}
		return "关"
	case 2:
		return strconv.Itoa(s.draft.DelayMS)
	}
	return ""
}
func (s *settingsState) apply(index int, raw string) error {
	value := strings.TrimSpace(raw)
	switch index {
	case 0:
		s.draft.Proxy = value
	case 1:
		s.draft.AllowSubmit = value == "开"
	case 2:
		n, e := strconv.Atoi(value)
		if e != nil || n < 0 || n > 60000 {
			return fmt.Errorf("请输入 0–60000 毫秒")
		}
		s.draft.DelayMS = n
	}
	return nil
}

func (m *Model) keySettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Up):
		m.settings.list.Move(-1)
	case key.Matches(msg, m.keys.Down):
		m.settings.list.Move(1)
	case key.Matches(msg, m.keys.Top):
		m.settings.list.GotoTop()
	case key.Matches(msg, m.keys.End):
		m.settings.list.GotoBottom()
	case key.Matches(msg, m.keys.PageUp):
		m.settings.list.Page(-1)
	case key.Matches(msg, m.keys.PageDown):
		m.settings.list.Page(1)
	case key.Matches(msg, m.keys.Space):
		m.toggleSetting()
	case key.Matches(msg, m.keys.Enter):
		if m.remote != nil && m.settings.list.cursor == 0 {
			m.pushToast(sevInfo, "使用 --server 地址连接另一服务端，--server=local 使用本地")
			return m, nil
		}
		if m.settingsCursorIsToggle() {
			m.toggleSetting()
			return m, nil
		}
		m.settings.editing = true
		m.settings.err = nil
		m.settings.status = ""
		m.settings.input.SetValue(m.settings.value(m.settings.list.cursor))
		m.settings.input.CursorEnd()
		return m, m.settings.input.Focus()
	case key.Matches(msg, m.keys.Save):
		return m, m.saveSettings()
	}
	return m, nil
}

// handleSettingsEditKey routes keys to the field editor while it is open.
func (m *Model) handleSettingsEditKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.settings.editing = false
		m.settings.input.Blur()
		return m, nil
	case tea.KeyEnter:
		index := m.settings.list.cursor

		value := m.settings.input.Value()
		m.settings.editing = false
		m.settings.input.Blur()
		if err := m.settings.apply(index, value); err != nil {
			m.settings.err = &AppError{Kind: kindInput, Short: settingFields[index].Label + "：" + err.Error()}
			return m, nil
		}
		m.settings.err = nil
		m.settings.status = ""
		m.settings.dirty = true
		return m, nil
	}
	var cmd tea.Cmd
	m.settings.input, cmd = m.settings.input.Update(msg)
	return m, cmd
}

func (m *Model) saveSettings() tea.Cmd {
	m.settings.err = nil
	m.settings.status = ""
	if err := m.settings.draft.Validate(); err != nil {
		m.settings.err = classify(err)
		return nil
	}
	if m.db != nil && m.remote == nil {
		if e := m.db.SetEnabled(m.settings.draft.AllowSubmit); e != nil {
			m.settings.err = classify(e)
			return nil
		}
	}
	return m.cmdSaveConfig(m.settings.draft)
}

var _ = fmt.Sprintf

func isSettingsKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "j", "k", "up", "down", "g", "G", "pgup", "pgdown", "enter", " ", "ctrl+s":
		return true
	}
	return msg.Type == tea.KeySpace
}
