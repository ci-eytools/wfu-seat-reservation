package tui

import (
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
)

// Glyphs centralises every non-ASCII character the UI uses so a terminal
// without reliable Unicode support can fall back to plain ASCII. Nerd Font
// private-use glyphs are deliberately avoided.
type Glyphs struct {
	Selected   string
	Unselected string
	Check      string
	Cross      string
	Dot        string
	Ring       string
	Arrow      string
	Bullet     string
	Ellipsis   string

	// Rule is the horizontal separator character.
	Rule string

	// Border is the single source of frame characters, shared by panels and
	// overlays: the thin box-drawing set with rounded corners (╭ ╮ ╰ ╯ ─ │ ├ ┤ ┬ ┴ ┼),
	// or its ASCII fallback when the terminal cannot render Unicode.
	Border lipgloss.Border

	// Spinner is the activity indicator style.
	Spinner spinner.Spinner
}

func newGlyphs(ascii bool) Glyphs {
	if ascii {
		return Glyphs{
			Selected:   ">",
			Unselected: " ",
			Check:      "v",
			Cross:      "x",
			Dot:        "*",
			Ring:       "o",
			Arrow:      "->",
			Bullet:     "-",
			Ellipsis:   "...",
			Rule:       "-",
			Border: lipgloss.Border{
				Top: "-", Bottom: "-", Left: "|", Right: "|",
				TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
				MiddleLeft: "+", MiddleRight: "+", Middle: "-",
				MiddleTop: "+", MiddleBottom: "+",
			},
			Spinner: spinner.Line,
		}
	}
	return Glyphs{
		Selected:   "▸",
		Unselected: " ",
		Check:      "✓",
		Cross:      "✗",
		Dot:        "●",
		Ring:       "○",
		Arrow:      "→",
		Bullet:     "·",
		Ellipsis:   "…",
		Rule:       "─",
		Border:     lipgloss.RoundedBorder(),
		Spinner:    spinner.MiniDot,
	}
}
