package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// formMinBreak is the narrowest wrap width allowed for the prepared form.
const formMinBreak = 24

// notSubmittedNote is the standing reminder without which a prepared encoding
// could be mistaken for a confirmation of an actual reservation.
const notSubmittedNote = "本地签名；按 enter 提交前会再确认一次"

// selectionFormBody renders the prepared signature form in its own view.
func (m *Model) selectionFormBody(width, height int, focused bool) []string {
	if m.selection == nil {
		return toLines(m.emptyView("没有可显示的表单",
			"在 "+paneTitle(PaneSeats)+" 选中一个空闲座位后按 enter。", width, height), height)
	}
	return toLines(m.form.View(), height)
}

// syncFormContent rewraps the prepared form. The form is one long query string
// with no spaces, so it is broken before each '&' to stay readable.
func (m *Model) syncFormContent() {
	if m.selection == nil {
		m.form.SetContent("")
		return
	}
	m.form.SetContent(breakQuery(m.selection.PreparedForm(), max(m.form.Width, formMinBreak)))
}

// breakQuery wraps a query string at parameter boundaries. The separator stays at
// the end of the line it closes, so removing the newlines reproduces the original
// query exactly -- a dropped '&' would silently show the user a wrong form.
func breakQuery(query string, width int) string {
	if width < formMinBreak {
		width = formMinBreak
	}
	parts := strings.Split(query, "&")
	var b strings.Builder
	col := 0
	for i, part := range parts {
		if i > 0 {
			if col > 0 && col+1+len(part) > width {
				b.WriteString("&\n")
				b.WriteString(part)
				col = len(part)
				continue
			}
			b.WriteByte('&')
			col++
		}
		b.WriteString(part)
		col += len(part)
	}
	return b.String()
}

// keySelection scrolls the form view. The encoding is the whole reason the view
// exists, so every movement key is a scroll key here. space commits the
// reservation -- printing the request first -- and enter steps back out, which is
// the view's "next item" once there is nothing deeper to preview.
func (m *Model) keySelection(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.Space) {
		return m.beginSubmit()
	}
	if key.Matches(msg, m.keys.Enter) {
		m.closeOverlay()
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.Up):
		m.form.LineUp(1)
	case key.Matches(msg, m.keys.Down):
		m.form.LineDown(1)
	case key.Matches(msg, m.keys.Top):
		m.form.GotoTop()
	case key.Matches(msg, m.keys.End):
		m.form.GotoBottom()
	case key.Matches(msg, m.keys.PageUp):
		m.form.PageUp()
	case key.Matches(msg, m.keys.PageDown):
		m.form.PageDown()
	}
	return m, nil
}
