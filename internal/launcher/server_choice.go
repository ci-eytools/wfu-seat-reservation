package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"os"
	"path/filepath"
	"wfuseat/internal/api"
)

func SavedServer(root string) (string, error) {
	raw, e := os.ReadFile(filepath.Join(root, "client.json"))
	if errors.Is(e, os.ErrNotExist) {
		return "", nil
	}
	if e != nil {
		return "", e
	}
	var pref map[string]string
	if e = json.Unmarshal(raw, &pref); e != nil {
		return "", fmt.Errorf("后端地址配置损坏：%w", e)
	}
	return api.NormalizeEndpoint(pref["server"])
}
func RememberServer(root, endpoint string) (string, error) {
	normalized, e := api.NormalizeEndpoint(endpoint)
	if e != nil {
		return "", e
	}
	if e = os.MkdirAll(root, 0700); e != nil {
		return "", e
	}
	raw, e := json.MarshalIndent(map[string]string{"server": normalized}, "", "  ")
	if e != nil {
		return "", e
	}
	file, e := os.CreateTemp(root, ".client-")
	if e != nil {
		return "", e
	}
	defer os.Remove(file.Name())
	if e = file.Chmod(0600); e == nil {
		_, e = file.Write(raw)
	}
	if e == nil {
		e = file.Sync()
	}
	closed := file.Close()
	if e != nil {
		return "", e
	}
	if closed != nil {
		return "", closed
	}
	if e = os.Rename(file.Name(), filepath.Join(root, "client.json")); e != nil {
		return "", e
	}
	return normalized, nil
}

type serverChoice struct {
	input     textinput.Model
	err       string
	selected  string
	cancelled bool
}

func newServerChoice(previous string) serverChoice {
	field := textinput.New()
	field.Prompt = "› "
	field.Placeholder = "https://seat.example.com"
	field.CharLimit = 2048
	field.Width = 65
	field.SetValue(previous)
	field.Focus()
	return serverChoice{input: field}
}
func (m serverChoice) Init() tea.Cmd { return textinput.Blink }
func (m serverChoice) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit
		case "enter":
			endpoint, e := api.NormalizeEndpoint(m.input.Value())
			if e != nil {
				m.err = e.Error()
				return m, nil
			}
			m.selected = endpoint
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}
func (m serverChoice) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("75")).Render("WFU 座位预约 · 后端连接")
	body := title + "\n\n请输入后端服务地址\n\n" + m.input.View() + "\n\nEnter 保存并进入  ·  Esc 退出\n地址会自动记住；下次直接启动即可。\n本机示例：http://127.0.0.1:8787"
	if m.err != "" {
		body += "\n\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render(m.err)
	}
	return lipgloss.NewStyle().Padding(2, 3).Render(body)
}
func ResolveServer(ctx context.Context, root, explicit string, choose, query bool) (string, error) {
	if explicit != "" {
		return RememberServer(root, explicit)
	}
	saved, e := SavedServer(root)
	if e != nil && !choose {
		return "", e
	}
	if saved != "" && !choose {
		return saved, nil
	}
	if query {
		return "", fmt.Errorf("尚未选择后端，请先启动界面配置，或使用 --server URL")
	}
	final, e := tea.NewProgram(newServerChoice(saved), tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	if e != nil {
		return "", e
	}
	choice, ok := final.(serverChoice)
	if !ok || choice.cancelled || choice.selected == "" {
		return "", nil
	}
	return RememberServer(root, choice.selected)
}
