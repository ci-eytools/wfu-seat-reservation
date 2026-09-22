package tui

import (
	"context"
	tea "github.com/charmbracelet/bubbletea"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
	"wfuseat/internal/chaoxing"
	"wfuseat/internal/config"
	"wfuseat/internal/schedule"
	"wfuseat/internal/storage"
)

func accountModel(t *testing.T, root, id string, fake http.RoundTripper) *Model {
	t.Helper()
	cfg, path, err := config.LoadAccount(root, id)
	if err != nil {
		t.Fatal(err)
	}
	session, err := chaoxing.NewSessionWithConfig(chaoxing.SessionConfig{BaseTransport: fake})
	if err != nil {
		t.Fatal(err)
	}
	login := &chaoxing.QRLogin{Session: session, Tokens: map[string]string{}}
	m, err := New(cfg, path, WithSession(login))
	if err != nil {
		t.Fatal(err)
	}
	m.qrWindowLauncher = func(context.Context, [][]bool) error { return context.Canceled }
	t.Cleanup(m.Close)
	m.Update(tea.WindowSizeMsg{Width: 130, Height: 40})
	return m
}
func TestAccountSessionRestoresWithoutQR(t *testing.T) {
	root := t.TempDir()
	first := accountModel(t, root, "alice", &apiFake{})
	u, _ := url.Parse("https://office.chaoxing.com/")
	first.login.Session.Client.Jar.SetCookies(u, []*http.Cookie{{Name: "UID", Value: "alice-secret", Path: "/", Secure: true}})
	raw, err := first.db.Get("cookies")
	if err != nil || len(raw) == 0 {
		t.Fatal("cookie not saved", err)
	}
	first.Close()
	fake := &apiFake{}
	second := accountModel(t, root, "alice", fake)
	if !second.restoreSession {
		t.Fatal("missing restore flag")
	}
	drain(t, second, second.beginOpenHome(), 20)
	if second.session.phase != phaseConfirmed {
		t.Fatal("session not verified", second.session.err)
	}
	for _, p := range fake.requestedPaths() {
		if strings.Contains(p, "ssoApi") {
			t.Fatal("requested QR on restore")
		}
	}
	bob := accountModel(t, root, "bob", &apiFake{})
	if bob.restoreSession || len(bob.login.Session.Client.Jar.Cookies(u)) != 0 {
		t.Fatal("alice cookie leaked to bob")
	}
}
func TestExpiredStoredSessionReturnsToLogin(t *testing.T) {
	m := accountModel(t, t.TempDir(), "alice", &apiFake{badSeatPage: true})
	drain(t, m, m.beginOpenHome(), 10)
	if m.session.phase != phaseFailed || m.session.busy {
		t.Fatal("failed session not recoverable")
	}
}
func TestMultiSelectPreservesOrderAndLimit(t *testing.T) {
	m := accountModel(t, t.TempDir(), "alice", &apiFake{})
	for _, s := range []string{"003", "001", "002", "004"} {
		m.toggleCandidate(s)
	}
	if !reflect.DeepEqual(m.candidates, []string{"003", "001", "002"}) {
		t.Fatal(m.candidates)
	}
	m.toggleCandidate("001")
	m.toggleCandidate("004")
	if !reflect.DeepEqual(m.candidates, []string{"003", "002", "004"}) {
		t.Fatal(m.candidates)
	}
	m.room.slots = []chaoxing.Slot{{StartTime: "19:00", EndTime: "19:30"}, {StartTime: "19:30", EndTime: "20:00"}}
	m.selectSlot(1)
	if len(m.candidates) != 0 {
		t.Fatal("stale candidates survived period change")
	}
}
func TestAccountIgnoresAnotherAccountsAsyncJobs(t *testing.T) {
	m := accountModel(t, t.TempDir(), "bob", &apiFake{})
	m.Update(jobsMsg{account: "alice", items: []storage.Job{{ID: "secret"}}})
	if len(m.jobs) != 0 {
		t.Fatal("late job response leaked")
	}
	m.Update(configSavedMsg{account: "alice", path: "alice-config"})
	if m.cfgPath == "alice-config" {
		t.Fatal("late config leaked")
	}
}
func TestAutoRowFirstRadioSelectionAndDuration(t *testing.T) {
	m := accountModel(t, t.TempDir(), "alice", &apiFake{})
	m.session.phase = phaseConfirmed
	m.rooms.day = time.Now().In(schedule.Zone).Format("2006-01-02")
	m.days.anchor = m.rooms.day
	m.focusSide(PanePeriods)
	m.setDayRow(dayRows)
	rows := m.periodsBodyLines(40, 8, true)
	if !strings.Contains(rows[0], "自动") {
		t.Fatal(rows)
	}
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if !m.repeatEnabled || m.zone != ZoneSide {
		t.Fatal("space must select without entering detail")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.zone != ZoneMain || m.repeatFocus != 1 {
		t.Fatal("enter must focus rules")
	}
	lines := strings.Join(m.periodDetailBody(110, 25, true), "\n")
	for _, want := range []string{"每天", "周一～周五", "按周循环", "开始日期", "结束条件"} {
		if !strings.Contains(lines, want) {
			t.Fatal(lines)
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyTab}) // start date
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.repeatInput.SetValue("2030-01-01")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.repeatStart != "2030-01-01" || m.repeatEditing {
		t.Fatal("start date not saved")
	}
	m.zone = ZoneSide
	m.moveDayRow(1)
	if m.days.cursor != 0 || !m.repeatEnabled {
		t.Fatal("moving to today must only preview")
	}
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if m.repeatEnabled {
		t.Fatal("specific date must disable automatic mode")
	}
	m.moveDayRow(-1)
	if m.days.cursor != dayRows {
		t.Fatal("up must reach first automatic row")
	}
}

// fallbackFake uses the production HTTP guard/parser/signer, but every request
// is intercepted here. No socket can be opened.
type fallbackFake struct {
	base  apiFake
	seats []string
	mode  string
}

func (f *fallbackFake) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == reserveTestPath {
		r.ParseForm()
		f.seats = append(f.seats, r.Form.Get("seatNum"))
		if r.Form.Get("enc") == "" {
			panic("missing fresh signature")
		}
		if f.mode == "unknown" {
			return textResponse(r, 200, `{"msg":"unknown"}`), nil
		}
		if len(f.seats) == 1 {
			return textResponse(r, 200, `{"status":false,"msg":"occupied"}`), nil
		}
		return textResponse(r, 200, `{"status":true}`), nil
	}
	return f.base.RoundTrip(r)
}
func TestDurableExecutorFullChainWithFallback(t *testing.T) {
	for _, mode := range []string{"refuse", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			fake := &fallbackFake{mode: mode}
			m := accountModel(t, t.TempDir(), "alice", fake)
			m.db.Put("cookies", []byte("{}"))
			j := storage.Job{RoomID: 6299, Day: seatDay, Start: "19:00", End: "19:30", Seats: []string{"002", "001", "003"}, Next: time.Now()}
			out := schedule.ExecuteWithLogin(context.Background(), m.cfg, m.db.(*storage.Store), j, m.login)
			want := []string{"002", "001"}
			state := "succeeded"
			if mode == "unknown" {
				want = want[:1]
				state = "unknown"
			}
			if out.State != state || !reflect.DeepEqual(fake.seats, want) {
				t.Fatalf("%+v requests=%v", out, fake.seats)
			}
		})
	}
}

func TestTUISchedulesPersistsAndCancelsWithoutSending(t *testing.T) {
	root := t.TempDir()
	fake := &apiFake{}
	m := accountModel(t, root, "alice", fake)
	next, err := schedule.Next("", "08:00:00", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, e := m.client.OpenHome(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e := m.client.ListRooms(context.Background(), next.In(schedule.Zone).Format("2006-01-02")); e != nil {
		t.Fatal(e)
	}
	if _, e := m.client.OpenRoom(context.Background(), 6299); e != nil {
		t.Fatal(e)
	}
	before := len(fake.requestedPaths())
	m.room.id = 6299
	m.room.name = "test room"
	m.rooms.day = next.In(schedule.Zone).Format("2006-01-02")
	m.room.slots = []chaoxing.Slot{{StartTime: "19:00", EndTime: "19:30"}}
	m.candidates = []string{"003", "001", "002"}
	m.repeatEnabled = true
	m.repeatStart = m.rooms.day
	m.repeatMode = 1
	m.room.periodExplicit = true
	m.room.endpoints = []int{0}
	m.cfg.Cron = "0 0 * * *" // Legacy account defaults must not override the visible daily rule.
	m.beginScheduledJob()
	if m.overlay != OverlayConfirm || m.confirm.action != confirmSchedule {
		t.Fatal("not scheduled", m.toasts)
	}
	drain(t, m, func() tea.Msg { _, cmd := m.runConfirm(); return tea.BatchMsg{cmd} }, 10)
	jobs, err := m.db.Jobs()
	if err != nil || len(jobs) != 1 || jobs[0].State != "pending" {
		t.Fatalf("%+v %v", jobs, err)
	}
	if jobs[0].RepeatStart != m.rooms.day || !jobs[0].RepeatForever || jobs[0].RepeatUntil != "" || jobs[0].RepeatMode != "weekdays" || len(jobs[0].WeekPlan) != 7 || jobs[0].WeekPlan[5].Enabled || jobs[0].Cron != "" {
		t.Fatal("daily configuration not persisted", jobs)
	}
	if !reflect.DeepEqual(jobs[0].Seats, m.candidates) {
		t.Fatal(jobs)
	}
	if len(fake.requestedPaths()) != before {
		t.Fatal("scheduling sent a request")
	}
	m.Close()
	again := accountModel(t, root, "alice", &apiFake{})
	drain(t, again, again.cmdJobs(), 4)
	if len(again.jobs) != 1 {
		t.Fatal("job lost after restart")
	}
	again.preorders.list.SetCount(1)
	drain(t, again, again.cancelJob(), 4)
	jobs, _ = again.db.Jobs()
	if jobs[0].State != "cancelled" {
		t.Fatal(jobs[0])
	}
}

func TestCancelTargetsConfirmedJobAfterListReorder(t *testing.T) {
	m := accountModel(t, t.TempDir(), "alice", &apiFake{})
	for _, id := range []string{"first", "second"} {
		m.db.Add(storage.Job{ID: id, State: "pending", Next: time.Now()})
	}
	drain(t, m, m.cmdJobs(), 3)
	m.preorders.list.cursor = 0
	m.beginCancelPreorder()
	m.jobs[0], m.jobs[1] = m.jobs[1], m.jobs[0]
	_, cmd := m.runConfirm()
	drain(t, m, cmd, 4)
	jobs, _ := m.db.Jobs()
	states := map[string]string{}
	for _, j := range jobs {
		states[j.ID] = j.State
	}
	if states["first"] != "cancelled" || states["second"] != "pending" {
		t.Fatal(states)
	}
}

func TestAccountPanelRendersWithoutNotifications(t *testing.T) {
	m := accountModel(t, t.TempDir(), "张20260001", &apiFake{})
	m.Update(keyMsg('u'))
	if m.side != PaneStatus || m.overlay != OverlayNone {
		t.Fatal("account panel not opened")
	}
	if m.toastLine() != "" {
		t.Fatal("unexpected toast")
	}
	if !strings.Contains(m.View(), "账号详情") {
		t.Fatal("missing detail")
	}
	m.pushToast(sevInfo, "test")
	m.expireToasts(time.Now().Add(toastTTL + time.Second))
	_ = m.View()
}

func TestPeriodEndpointsIncludeBothAndRejectGap(t *testing.T) {
	m := accountModel(t, t.TempDir(), "张20260001", &apiFake{})
	m.room.slots = []chaoxing.Slot{{StartTime: "09:00", EndTime: "09:30"}, {StartTime: "09:30", EndTime: "10:00"}, {StartTime: "10:00", EndTime: "10:30"}, {StartTime: "11:00", EndTime: "11:30"}}
	m.selectSlot(2)
	m.selectPeriodFromGrid()
	m.selectSlot(0)
	m.selectPeriodFromGrid()
	slot, ok := m.room.currentSlot()
	if !ok || slot.StartTime != "09:00" || slot.EndTime != "10:30" {
		t.Fatal(slot, ok)
	}
	m.candidates = []string{"001"}
	m.selectSlot(1)
	if len(m.candidates) != 1 {
		t.Fatal("cursor changed committed selection")
	}
	m.selectPeriodFromGrid()
	if len(m.room.endpoints) != 2 {
		t.Fatal("third endpoint accepted")
	}
	m.selectSlot(0)
	m.selectPeriodFromGrid()
	m.selectSlot(3)
	m.selectPeriodFromGrid()
	if len(m.room.endpoints) != 1 {
		t.Fatal("gap accepted")
	}
	m.selectSlot(2)
	m.selectPeriodFromGrid()
	if _, ok = m.room.currentSlot(); ok {
		t.Fatal("deselected interval still active")
	}
}
