package tui

import (
	"strconv"

	"github.com/charmbracelet/bubbles/key"
)

// KeyMap is the single source of truth for every binding. The context help bar
// and the help modal are both derived from it, so the documented shortcuts cannot
// drift away from the implemented ones.
//
// Two ideas keep it predictable:
//
//	j / k (and ↓ / ↑)  move inside the focused panel, and nothing else
//	h / l (and ← / →)  move between panels, and nothing else
//
// No key ever means "move a cursor" in one place and "change panel" in another,
// which is why lists are vertical: a list must never need horizontal cursor keys.
type KeyMap struct {
	Up    key.Binding
	Down  key.Binding
	Left  key.Binding
	Right key.Binding
	Top   key.Binding
	End   key.Binding

	PageUp   key.Binding
	PageDown key.Binding

	NextPanel key.Binding
	PrevPanel key.Binding

	Enter   key.Binding
	Back    key.Binding
	Space   key.Binding
	Filter  key.Binding
	Refresh key.Binding
	Scan    key.Binding
	Help    key.Binding
	Quit    key.Binding
	Force   key.Binding

	Edit key.Binding
	Save key.Binding

	// Logs and Settings open the two utility views. They are views rather than
	// panels because their content has nothing to do with the selection chain.
	Logs key.Binding

	// the room has no bookable period left.

	// Preview focuses the selection card, which sits outside the panel ring and so
	// has its own number.
	Preview key.Binding

	// Auto schedules a request for the moment the reservation window opens. The
	// shot it takes is always a simulation; sending for real stays manual.
	Auto key.Binding

	// DayNext, DayPrev and DayToday move the date being planned. A day whose
	// periods have all passed is not the end of the session: the next day's
	// window is usually not open yet, which is exactly what scheduling is for.
	DayNext  key.Binding
	DayPrev  key.Binding
	DayToday key.Binding

	// Panes is the fast path to a panel, numbered the way lazygit numbers its
	// side column: the digit is printed in the panel's own title, so the binding
	// is discoverable without opening the help.
	Panes []key.Binding

	// Compact bindings exist only for the context help bar, which has one line and
	// must stay scannable; the help modal uses the full-length bindings above.
	HintPanels key.Binding
	HintMove   key.Binding
	HintOpen   key.Binding
	HintSelect key.Binding
	HintScroll key.Binding
}

func newKeyMap() KeyMap {
	k := KeyMap{
		Up:    key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "上移")),
		Down:  key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "下移")),
		Left:  key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "往左一层")),
		Right: key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "往右一层")),
		Top:   key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "顶部")),
		End:   key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "底部")),

		PageUp:   key.NewBinding(key.WithKeys("pgup", "ctrl+u"), key.WithHelp("PgUp/^u", "上翻页")),
		PageDown: key.NewBinding(key.WithKeys("pgdown", "ctrl+d"), key.WithHelp("PgDn/^d", "下翻页")),

		NextPanel: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "下一面板")),
		PrevPanel: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "上一面板")),

		Enter:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "打开/确认")),
		Back:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "返回上一级")),
		Space:   key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "选中/切换")),
		Filter:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "过滤")),
		Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "刷新")),
		Scan:    key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "扫描时段")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "帮助")),
		Quit:    key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "退出")),
		Force:   key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "强制退出")),

		Edit: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "编辑")),
		Save: key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "保存")),

		Logs: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "诊断")),
		Auto: key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "创建预订")),

		DayNext:  key.NewBinding(key.WithKeys("]"), key.WithHelp("]", "后一天")),
		DayPrev:  key.NewBinding(key.WithKeys("["), key.WithHelp("[", "前一天")),
		DayToday: key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "服务器当天")),
		Preview: key.NewBinding(
			key.WithKeys(strconv.Itoa(int(PaneDetails))),
			key.WithHelp(strconv.Itoa(int(PaneDetails)), "选座预览"),
		),

		HintPanels: displayHint("←→/hl", "面板"),
		HintMove:   displayHint("↑↓/jk", "移动"),
		HintOpen:   displayHint("enter", "打开/进入"),
		HintSelect: displayHint("space", "选中"),
		HintScroll: displayHint("↑↓/jk", "滚动"),
	}

	// One binding per panel, keyed by the number printed in its title.
	k.Panes = make([]key.Binding, 0, len(sidePanes))
	for i, side := range sidePanes {
		_ = i
		k.Panes = append(k.Panes, key.NewBinding(
			key.WithKeys(strconv.Itoa(int(side))),
			key.WithHelp(strconv.Itoa(int(side)), side.title()),
		))
	}
	return k
}

// bindingGroup is one titled group of shortcuts, used by the help modal.
type bindingGroup struct {
	Title string
	Keys  []key.Binding
}

// globalGroups are the shortcuts that are available everywhere, grouped by what
// they do rather than dumped as one list.
func (k KeyMap) globalGroups() []bindingGroup {
	return []bindingGroup{
		{Title: "导航", Keys: []key.Binding{k.Down, k.Up, k.Right, k.Left, k.Top, k.End}},
		{Title: "面板", Keys: append(append([]key.Binding{k.NextPanel, k.PrevPanel}, k.Panes...), k.Preview)},
		{Title: "层级", Keys: []key.Binding{k.Back, k.Left, k.Right}},
		{Title: "滚动", Keys: []key.Binding{k.PageDown, k.PageUp}},
		{Title: "操作", Keys: []key.Binding{k.Enter, k.Space, k.Filter, k.Refresh, k.Scan, k.Auto}},
		{Title: "日期", Keys: []key.Binding{k.DayNext, k.DayPrev, k.DayToday}},
		{Title: "视图", Keys: []key.Binding{k.Logs}},
		{Title: "程序", Keys: []key.Binding{k.Help, k.Quit, k.Force}},
	}
}

// displayHint builds a binding that exists only to be shown in the help bar, so a
// combined hint can be rendered without inventing a real key.
func displayHint(keys, desc string) key.Binding {
	return key.NewBinding(key.WithHelp(keys, desc))
}

// hint is the compact "key  description" fragment used by the help bar.
func hint(b key.Binding) string {
	h := b.Help()
	return h.Key + " " + h.Desc
}
