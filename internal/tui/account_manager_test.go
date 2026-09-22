package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wfuseat/internal/chaoxing"
	"wfuseat/internal/config"
	"wfuseat/internal/storage"
)

func TestAuthenticatedDefaultMigratesToServerIdentity(t *testing.T) {
	root := t.TempDir()
	m := accountModel(t, root, "default", &apiFake{})
	m.db.Put("cookies", []byte(`{}`))
	m.db.Add(storage.Job{ID: "kept", State: "pending", Next: time.Now().Add(time.Hour)})
	m.login.SeatPageResponse = &chaoxing.Response{Body: []byte(`var userLoginInfo={"userInfo":{"uname":"张三","sno":"20260001"}}||{}`)}
	next, _ := m.onHomeOpened(homeOpenedMsg{gen: m.genSession, client: m.client, info: &chaoxing.HomeInfo{ServerDay: "2026-09-18"}})
	n := next.(*Model)
	defer n.Close()
	if !n.ready || n.width != m.width || n.height != m.height || n.View() == "" {
		t.Fatal("migration lost terminal state")
	}
	if n.cfg.Account != "张20260001" {
		t.Fatal(n.cfg.Account, n.toasts, m.client.AccountName())
	}
	if _, e := os.Stat(filepath.Join(root, "accounts", "default")); !os.IsNotExist(e) {
		t.Fatal("default remains")
	}
	jobs, _ := n.db.Jobs()
	if len(jobs) != 1 || jobs[0].ID != "kept" {
		t.Fatal("migration lost job")
	}
}
func TestAccountControlsPauseAndShowOnlyRelevantOptions(t *testing.T) {
	m := accountModel(t, t.TempDir(), "张20260001", &apiFake{})
	m.focusSide(PaneStatus)
	m.zone = ZoneMain
	_, cmd := m.Update(keyMsg('p'))
	drain(t, m, cmd, 4)
	if m.db.Enabled() || m.cfg.AllowSubmit {
		t.Fatal("switch not persisted")
	}
	m.session.phase = phaseConfirmed
	view := strings.Join(m.statusContentBody(90, 20, true), "\n")
	if strings.Contains(view, "FID") || strings.Contains(view, "全局执行") {
		t.Fatal(view)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.zone != ZoneSide {
		t.Fatal("escape did not return to account list")
	}
	m.zone = ZoneMain
	m.Update(keyMsg('D'))
	if m.confirm.action != confirmDeleteAccount {
		t.Fatal("missing deletion review")
	}
}

func TestNewAccountKeepsTerminalStateAndRendersQR(t *testing.T) {
	m := accountModel(t, t.TempDir(), "张20260001", &apiFake{})
	m.focusSide(PaneStatus)
	handled, next, _ := m.accountControl(keyMsg('n'))
	n := next.(*Model)
	defer n.Close()
	if !handled || n == m || !n.ready || n.View() == "" {
		t.Fatal("new account rendered blank")
	}
	n.onQRStarted(qrStartedMsg{gen: n.genSession, png: testQRPNG(t)})
	if len(n.session.qrGrid) == 0 || n.View() == "" {
		t.Fatal("QR missing after account replacement")
	}
}
func TestAccountSidePanelCannotToggleBooking(t *testing.T) {
	m := accountModel(t, t.TempDir(), "张20260001", &apiFake{})
	m.focusSide(PaneStatus)
	before := m.cfg.AllowSubmit
	m.Update(keyMsg('p'))
	if m.cfg.AllowSubmit != before {
		t.Fatal("sidebar changed booking switch")
	}
	m.Update(keyMsg('e'))
	if m.settings.editing {
		t.Fatal("sidebar edited options")
	}
}

func TestRandomDelayOptionReplacesASCII(t *testing.T) {
	s := newSettingsState(config.Default())
	if settingFields[2].Kind != settingText || strings.Contains(settingFields[2].Label, "ASCII") {
		t.Fatal(settingFields[2])
	}
	if e := s.apply(2, "250"); e != nil || s.draft.DelayMS != 250 || s.value(2) != "250" {
		t.Fatal(e, s.value(2))
	}
	if e := s.apply(2, "-1"); e == nil || s.draft.DelayMS != 250 {
		t.Fatal("bad delay changed config")
	}
}

func TestCancelNewAccountRemovesDraftAndRestoresOriginal(t *testing.T) {
	root := t.TempDir()
	m := accountModel(t, root, "张20260001", &apiFake{})
	m.session.phase = phaseConfirmed
	m.focusSide(PaneStatus)
	_, model, _ := m.accountControl(keyMsg('n'))
	draft := model.(*Model)
	if draft == m || draft.zone != ZoneMain || !draft.session.busy {
		t.Fatal("n did not open QR detail")
	}
	id := draft.cfg.Account
	for _, v := range draft.accountIDs() {
		if strings.HasPrefix(v, "pending-") {
			t.Fatal("draft visible in account list")
		}
	}
	draft.onQRStarted(qrStartedMsg{gen: draft.genSession, png: testQRPNG(t)})
	draft.width = 150
	restored, _ := draft.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if restored != m || m.closed || m.zone != ZoneSide || m.width != 150 {
		t.Fatal("original account not restored")
	}
	dir := filepath.Join(root, "accounts", id)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("cancel left temporary account", err)
	}
	m.Update(homeOpenedMsg{gen: draft.genSession}) // A late callback must be ignored.
	if m.cfg.Account != "张20260001" || len(m.accountIDs()) != 1 {
		t.Fatal("late login leaked")
	}
	if _, err := m.db.Jobs(); err != nil {
		t.Fatal("original database closed", err)
	}
}
