package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"wfuseat/internal/config"
)

// logMaxEntries and logMaxFiles bound how much of the request log is loaded, so a
// long-running session cannot make this screen expensive to open.
const (
	logMaxEntries = 2000
	logMaxFiles   = 5
)

// logsColumns is the diagnostics view: the request log beside the selected
// record. It reuses the panel frame rather than floating a box, so it gets the
// whole terminal for a list that is genuinely long.
func (m *Model) logsColumns() []columnSpec {
	return []columnSpec{
		{width: m.viewPaneWidth(logListPaneWidthWide, logListPaneWidthNarrow),
			rows: []rowSpec{{slot: m.logsPanel()}}},
		{rows: []rowSpec{{slot: m.logDetailPanel()}}},
	}
}

func (m *Model) logsPanel() panelSlot {
	return panelSlot{
		owner:  panelOwner{zone: ZoneView, index: 0},
		title:  "请求记录",
		trail:  m.logsTrailing,
		status: m.logsStatusLine,
		body:   m.logsBodyLines,
	}
}

func (m *Model) logDetailPanel() panelSlot {
	return panelSlot{
		owner:  panelOwner{zone: ZoneView, index: 1},
		title:  "记录详情",
		trail:  func() string { return "已脱敏" },
		status: func() string { return "↑/↓ 滚动 · token 与 cookie 值已被抹除" },
		body:   m.logDetailBody,
	}
}

func (m *Model) logsDir() string {
	if m.cfg.LogDir != "" {
		return m.cfg.LogDir
	}
	return config.DefaultLogDir
}

func (m *Model) logsTrailing() string {
	if len(m.logs.entries) == 0 {
		return ""
	}
	position, total := m.logs.list.Scrolled()
	return m.listPosition(position, total)
}

func (m *Model) logsStatusLine() string {
	return fmt.Sprintf("%s · 拦截 %d 次", m.clips(m.logsDir(), 40), m.login.BlockedSeatRequests())
}

func (m *Model) clips(text string, width int) string { return m.clip(text, width) }

func (m *Model) logsBodyLines(width, height int, focused bool) []string {
	switch {
	case m.logs.loading:
		return toLines(m.loadingView("正在读取脱敏请求日志…", width, height), height)
	case m.logs.err != nil:
		return toLines(m.errorView(m.logs.err, width, height), height)
	case len(m.logs.entries) == 0:
		return toLines(m.emptyView("还没有请求日志",
			"完成一次请求后这里会出现脱敏记录。", width, height), height)
	}

	// Columns drop from the right as the panel narrows.
	showMethod := width >= 40
	showStatus := width >= 30
	const (
		timeWidth   = 9
		methodWidth = 6
		statusWidth = 5
	)
	fixed := timeWidth
	if showMethod {
		fixed += methodWidth
	}
	if showStatus {
		fixed += statusWidth
	}
	pathWidth := max(width-2-fixed, 10)

	first, last := m.logs.list.Window()
	lines := make([]string, 0, height)
	for i := first; i < last; i++ {
		entry := m.logs.entries[i]
		selected := i == m.logs.list.cursor
		stamp := "—"
		if !entry.Time.IsZero() {
			stamp = entry.Time.Local().Format("15:04:05")
		}
		cells := []listCell{m.mutedCell(stamp, timeWidth)}
		if showMethod {
			cells = append(cells, m.mutedCell(entry.Method, methodWidth))
		}
		cells = append(cells, m.plainCell(urlPath(entry.URL), pathWidth))
		if showStatus {
			status := "—"
			style := m.theme.BadgeMuted
			switch {
			case entry.Status >= 400:
				status, style = itoa(entry.Status), m.theme.BadgeErr
			case entry.Status >= 300:
				status, style = itoa(entry.Status), m.theme.BadgeWarn
			case entry.Status > 0:
				status, style = itoa(entry.Status), m.theme.BadgeOK
			}
			cells = append(cells, listCell{text: m.column(status, statusWidth), style: style})
		}
		lines = append(lines, m.listRow(width, selected, focused, cells...))
	}
	return padList(lines, height)
}

// logDetailBody pretty-prints the selected record. Values are already redacted by
// the logger, so nothing sensitive reaches the screen.
func (m *Model) logDetailBody(width, height int, focused bool) []string {
	m.logs.detail.SetContent(m.logDetailContent())
	return toLines(m.logs.detail.View(), height)
}

func (m *Model) logDetailContent() string {
	index, ok := m.logs.list.Selected()
	if !ok || index >= len(m.logs.entries) {
		return m.theme.FaintText.Render("（未选择记录）")
	}
	entry := m.logs.entries[index]
	if entry.Raw == nil {
		return m.theme.MutedText.Render(entry.RawLine)
	}
	pretty, err := json.MarshalIndent(entry.Raw, "", "  ")
	if err != nil {
		return m.theme.MutedText.Render(entry.RawLine)
	}
	return m.theme.MutedText.Render(string(pretty))
}

func (m *Model) keyLogs(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Up):
		m.logs.list.Move(-1)
	case key.Matches(msg, m.keys.Down):
		m.logs.list.Move(1)
	case key.Matches(msg, m.keys.Top):
		m.logs.list.GotoTop()
	case key.Matches(msg, m.keys.End):
		m.logs.list.GotoBottom()
	case key.Matches(msg, m.keys.PageUp):
		m.logs.list.Page(-1)
	case key.Matches(msg, m.keys.PageDown):
		m.logs.list.Page(1)
	case key.Matches(msg, m.keys.Enter):
		m.overlayFocus = 1
	}
	return m, nil
}

func (m *Model) keyLogDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Up):
		m.logs.detail.LineUp(1)
	case key.Matches(msg, m.keys.Down):
		m.logs.detail.LineDown(1)
	case key.Matches(msg, m.keys.Top):
		m.logs.detail.GotoTop()
	case key.Matches(msg, m.keys.End):
		m.logs.detail.GotoBottom()
	case key.Matches(msg, m.keys.PageUp):
		m.logs.detail.PageUp()
	case key.Matches(msg, m.keys.PageDown):
		m.logs.detail.PageDown()
	}
	return m, nil
}

// loadLogEntries reads the newest redacted log files, newest entry first.
func loadLogEntries(dir string) ([]logEntry, []string, error) {
	if strings.TrimSpace(dir) == "" {
		dir = config.DefaultLogDir
	}
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("无法读取日志目录 %s: %w", dir, err)
	}
	var files []string
	for _, entry := range dirEntries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		files = append(files, entry.Name())
	}
	// File names embed a UTC timestamp, so lexical order is chronological.
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	if len(files) > logMaxFiles {
		files = files[:logMaxFiles]
	}

	// Files are already newest-first, and lines inside a file are chronological,
	// so each file is walked backwards to produce a single newest-first list.
	var entries []logEntry
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		lines := strings.Split(string(raw), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			entries = append(entries, parseLogLine(line))
		}
	}
	if len(entries) > logMaxEntries {
		entries = entries[:logMaxEntries]
	}
	return entries, files, nil
}

func parseLogLine(line string) logEntry {
	entry := logEntry{RawLine: line}
	var object map[string]any
	if err := json.Unmarshal([]byte(line), &object); err != nil {
		return entry
	}
	entry.Raw = object
	if v, ok := object["time_utc"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, v); err == nil {
			entry.Time = parsed
		}
	}
	if v, ok := object["method"].(string); ok {
		entry.Method = v
	}
	if v, ok := object["url"].(string); ok {
		entry.URL = v
	}
	if v, ok := object["status"].(float64); ok {
		entry.Status = int(v)
	}
	if v, ok := object["elapsed_ms"].(float64); ok {
		entry.ElapsedMS = int64(v)
	}
	return entry
}

// urlPath reduces a safe URL to its path for the list column.
func urlPath(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Path == "" {
		return raw
	}
	return parsed.Path
}
