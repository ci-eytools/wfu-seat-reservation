package tui

import "github.com/charmbracelet/lipgloss"

// Theme holds the semantic colour tokens and the derived styles. Colours are
// declared as AdaptiveColor so lipgloss picks the dark or light variant for the
// terminal in use, and automatically degrades to 256- or 16-colour output when
// the terminal cannot render true colour.
//
// Colour here carries meaning only: primary for the active element, success for
// a confirmed state, warning for something the user should check, danger for a
// refused or failed operation, and the muted/faint greys for secondary text.
type Theme struct {
	Dark bool

	Primary    lipgloss.AdaptiveColor
	Success    lipgloss.AdaptiveColor
	Warning    lipgloss.AdaptiveColor
	Danger     lipgloss.AdaptiveColor
	Text       lipgloss.AdaptiveColor
	Muted      lipgloss.AdaptiveColor
	Faint      lipgloss.AdaptiveColor
	Border     lipgloss.AdaptiveColor
	SelectedBg lipgloss.AdaptiveColor

	// Structural styles.
	Header    lipgloss.Style
	Footer    lipgloss.Style
	AppName   lipgloss.Style
	Rule      lipgloss.Style
	Heading   lipgloss.Style
	Label     lipgloss.Style
	Value     lipgloss.Style
	MutedText lipgloss.Style
	FaintText lipgloss.Style
	Accent    lipgloss.Style
	// Committed is the seat or item a choice is in force for, as opposed to the one
	// the cursor merely sits on.
	Committed lipgloss.Style

	// List rows. Selection is expressed with both a marker and a background so it
	// survives a terminal with no colour, and the unfocused variant is weaker so
	// the eye stays on the focused panel.
	RowNormal      lipgloss.Style
	RowMuted       lipgloss.Style
	RowSelected    lipgloss.Style
	RowSelectedDim lipgloss.Style
	// RowCursor is the gutter marker of the selected row.
	RowCursor lipgloss.Style

	// Panels. Only the focused panel takes the accent colour, so the eye always
	// lands on the region that owns the keyboard.
	PanelBorder      lipgloss.Style
	PanelBorderFocus lipgloss.Style
	PanelTitle       lipgloss.Style
	PanelTitleFocus  lipgloss.Style
	PanelStatus      lipgloss.Style

	// Status badges.
	BadgeOK    lipgloss.Style
	BadgeWarn  lipgloss.Style
	BadgeErr   lipgloss.Style
	BadgeInfo  lipgloss.Style
	BadgeMuted lipgloss.Style

	// Overlays.
	Overlay      lipgloss.Style
	OverlayTitle lipgloss.Style
	OverlayHelp  lipgloss.Style
}

func newTheme() Theme {
	t := Theme{
		Dark:       lipgloss.HasDarkBackground(),
		Primary:    lipgloss.AdaptiveColor{Light: "#1F4FD8", Dark: "#7AA2F7"},
		Success:    lipgloss.AdaptiveColor{Light: "#1B7F3B", Dark: "#9ECE6A"},
		Warning:    lipgloss.AdaptiveColor{Light: "#8F5B00", Dark: "#E0AF68"},
		Danger:     lipgloss.AdaptiveColor{Light: "#B3261E", Dark: "#F7768E"},
		Text:       lipgloss.AdaptiveColor{Light: "#1F2328", Dark: "#C6D0F5"},
		Muted:      lipgloss.AdaptiveColor{Light: "#57606A", Dark: "#8C96C0"},
		Faint:      lipgloss.AdaptiveColor{Light: "#8C959F", Dark: "#545C7E"},
		Border:     lipgloss.AdaptiveColor{Light: "#D0D7DE", Dark: "#333A56"},
		SelectedBg: lipgloss.AdaptiveColor{Light: "#DDE7FF", Dark: "#2B3356"},
	}

	t.Header = lipgloss.NewStyle().Padding(0, 1)
	t.Footer = lipgloss.NewStyle().Padding(0, 1)
	t.AppName = lipgloss.NewStyle().Bold(true).Foreground(t.Primary)
	t.Rule = lipgloss.NewStyle().Foreground(t.Border)
	t.Heading = lipgloss.NewStyle().Bold(true).Foreground(t.Text)
	t.Label = lipgloss.NewStyle().Foreground(t.Muted)
	t.Value = lipgloss.NewStyle().Foreground(t.Text)
	t.MutedText = lipgloss.NewStyle().Foreground(t.Muted)
	t.FaintText = lipgloss.NewStyle().Foreground(t.Faint)
	t.Accent = lipgloss.NewStyle().Foreground(t.Primary)
	t.Committed = lipgloss.NewStyle().Foreground(t.Primary).Bold(true)

	t.RowNormal = lipgloss.NewStyle().Foreground(t.Text)
	t.RowMuted = lipgloss.NewStyle().Foreground(t.Muted)
	t.RowSelected = lipgloss.NewStyle().Foreground(t.Primary).Bold(true).Background(t.SelectedBg)
	t.RowSelectedDim = lipgloss.NewStyle().Foreground(t.Text).Background(t.SelectedBg)
	t.RowCursor = lipgloss.NewStyle().Foreground(t.Primary).Bold(true)

	t.PanelBorder = lipgloss.NewStyle().Foreground(t.Border)
	t.PanelBorderFocus = lipgloss.NewStyle().Foreground(t.Primary)
	t.PanelTitle = lipgloss.NewStyle().Foreground(t.Muted).Bold(true)
	t.PanelTitleFocus = lipgloss.NewStyle().Foreground(t.Primary).Bold(true)
	t.PanelStatus = lipgloss.NewStyle().Foreground(t.Muted)

	t.BadgeOK = lipgloss.NewStyle().Foreground(t.Success)
	t.BadgeWarn = lipgloss.NewStyle().Foreground(t.Warning)
	t.BadgeErr = lipgloss.NewStyle().Foreground(t.Danger)
	t.BadgeInfo = lipgloss.NewStyle().Foreground(t.Primary)
	t.BadgeMuted = lipgloss.NewStyle().Foreground(t.Faint)

	t.Overlay = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Border).
		Padding(1, 2)
	t.OverlayTitle = lipgloss.NewStyle().Bold(true).Foreground(t.Text)
	t.OverlayHelp = lipgloss.NewStyle().Foreground(t.Muted)

	return t
}

// panelBorder returns the frame style for a panel.
func (t Theme) panelBorder(focused bool) lipgloss.Style {
	if focused {
		return t.PanelBorderFocus
	}
	return t.PanelBorder
}

// panelTitle returns the title style for a panel.
func (t Theme) panelTitle(focused bool) lipgloss.Style {
	if focused {
		return t.PanelTitleFocus
	}
	return t.PanelTitle
}

// severity colours a message consistently across every screen.
type severity int

const (
	sevNone severity = iota
	sevInfo
	sevOK
	sevWarn
	sevErr
)

func (t Theme) severityStyle(s severity) lipgloss.Style {
	switch s {
	case sevOK:
		return lipgloss.NewStyle().Foreground(t.Success)
	case sevWarn:
		return lipgloss.NewStyle().Foreground(t.Warning)
	case sevErr:
		return lipgloss.NewStyle().Foreground(t.Danger)
	case sevInfo:
		return lipgloss.NewStyle().Foreground(t.Primary)
	default:
		return lipgloss.NewStyle().Foreground(t.Muted)
	}
}
