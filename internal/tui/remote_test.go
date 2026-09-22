package tui

import (
	"context"
	"encoding/json"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"wfuseat/internal/api"
	"wfuseat/internal/apiserver"
	"wfuseat/internal/chaoxing"
	"wfuseat/internal/config"
	"wfuseat/internal/schedule"
	"wfuseat/internal/storage"
)

// All school traffic terminates here, including the synthetic submit endpoint.
// Each new transport must prove its identity with the restored school cookie.
type identitySchool struct {
	traceMu sync.Mutex
	agents  []string
	base    http.RoundTripper
	student string
	scanned bool
}

func (f *identitySchool) RoundTrip(r *http.Request) (*http.Response, error) {
	f.traceMu.Lock()
	f.agents = append(f.agents, r.Header.Get("User-Agent"))
	f.traceMu.Unlock()
	if r.URL.Path == "/OAuth2/wfu/login" {
		f.scanned = true
	}
	if r.URL.Path == "/front/third/apps/seat/index" {
		student := ""
		if cookie, e := r.Cookie("UID"); e == nil {
			student = cookie.Value
		}
		if student == "" && f.scanned {
			student = f.student
		}
		if student == "" {
			return textResponse(r, 403, "login required"), nil
		}
		page := strings.Replace(seatIndexPage, `var userLoginInfo = { "userInfo": { "name": "test" } };`, fmt.Sprintf(`<script>var userLoginInfo = {"userInfo":{"uname":"张三","sno":"%s"}} || {};</script>`, student), 1)
		response := textResponse(r, 200, page)
		response.Header.Add("Set-Cookie", "UID="+student+"; Path=/; Secure; HttpOnly; SameSite=Lax")
		return response, nil
	}
	return f.base.RoundTrip(r)
}

type remoteFixture struct {
	traces           []*identitySchool
	root, clientRoot string
	server           *apiserver.Server
	http             *httptest.Server
	base             *fallbackFake
	mu               sync.Mutex
	student          string
}

func newRemoteFixture(t *testing.T) *remoteFixture {
	t.Helper()
	f := &remoteFixture{root: t.TempDir(), clientRoot: t.TempDir(), student: "20260001", base: &fallbackFake{mode: "refuse", base: apiFake{qrPNG: testQRPNG(t)}}}
	var err error
	f.server, err = apiserver.New(apiserver.Options{Root: f.root, NewLogin: f.login})
	if err != nil {
		t.Fatal(err)
	}
	f.http = httptest.NewServer(f.server)
	t.Cleanup(func() { f.http.Close(); f.server.Close() })
	return f
}
func (f *remoteFixture) login() (*chaoxing.QRLogin, error) {
	f.mu.Lock()
	student := f.student
	f.mu.Unlock()
	transport := &identitySchool{base: f.base, student: student}
	f.mu.Lock()
	f.traces = append(f.traces, transport)
	f.mu.Unlock()
	session, e := chaoxing.NewSessionWithConfig(chaoxing.SessionConfig{BaseTransport: transport})
	if e != nil {
		return nil, e
	}
	return &chaoxing.QRLogin{Session: session, Tokens: map[string]string{}}, nil
}
func (f *remoteFixture) client(t *testing.T, student string) *api.Client {
	t.Helper()
	f.mu.Lock()
	f.student = student
	f.mu.Unlock()
	c, e := api.NewClient(f.http.URL, f.clientRoot)
	if e != nil {
		t.Fatal(e)
	}
	png, e := c.Start(context.Background())
	if e != nil || len(png) == 0 {
		t.Fatalf("QR %v", e)
	}
	state, e := c.Poll(context.Background())
	if e != nil || state != chaoxing.QRConfirmed {
		t.Fatalf("poll %s %v", state, e)
	}
	return c
}
func remoteToken(t *testing.T, c *api.Client) string {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(c.ProfileRoot, "accounts", c.Identity().ID, "session.json"))
	if e != nil {
		t.Fatal(e)
	}
	var v api.Credentials
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	return v.Token
}
func rawRemote(t *testing.T, base, method, path, token, key, body string) (int, []byte) {
	t.Helper()
	r, e := http.NewRequest(method, base+path, strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Idempotency-Key", key)
	res, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	b, e := io.ReadAll(res.Body)
	if e != nil {
		t.Fatal(e)
	}
	return res.StatusCode, b
}

func TestRemoteFullChainAndTenantIsolation(t *testing.T) {
	f := newRemoteFixture(t)
	a := f.client(t, "20260001")
	b := f.client(t, "20260002")
	if a.Identity().ID == b.Identity().ID || a.Identity().DisplayName != "张20260001" {
		t.Fatal("identity isolation")
	}
	ctx := context.Background()
	c := chaoxing.NewSeatClient(nil, config.DefaultFIDEnc, config.DefaultMappID)
	c.Remote = a
	if _, e := c.OpenHome(ctx); e != nil {
		t.Fatal(e)
	}
	day := time.Now().In(schedule.Zone).AddDate(0, 0, 1).Format("2006-01-02")
	if rooms, e := c.ListRooms(ctx, day); e != nil || len(rooms) != 2 {
		t.Fatalf("rooms %v %v", rooms, e)
	}
	if room, e := c.OpenRoom(ctx, 6299); e != nil || len(room.Slots) == 0 {
		t.Fatalf("room %v %v", room, e)
	}
	if seats, e := c.SeatMap(ctx, "19:00", "19:30"); e != nil || len(seats.Seats) != 3 {
		t.Fatalf("seats %v %v", seats, e)
	}
	if e := a.Settings(ctx, api.Settings{AllowSubmit: true, DelayMS: 7}); e != nil {
		t.Fatal(e)
	}
	me, e := b.Me(ctx)
	if e != nil || me.Settings.DelayMS != 0 {
		t.Fatalf("settings leaked %v %v", me, e)
	}
	job := storage.Job{ID: "retry-safe-request", RoomID: 6299, Day: day, Start: "19:00", End: "19:30", Seats: []string{"002", "001"}}
	if e := a.Add(job); e != nil {
		t.Fatal(e)
	}
	if e := a.Add(job); e != nil {
		t.Fatal("idempotent replay", e)
	}
	jobs, e := a.Jobs()
	if e != nil || len(jobs) != 1 {
		t.Fatalf("jobs %v %v", jobs, e)
	}
	job.Seats = []string{"001"}
	if e := a.Add(job); e == nil {
		t.Fatal("changed replay accepted")
	}
	if other, e := b.Jobs(); e != nil || len(other) != 0 {
		t.Fatal("tenant jobs leaked")
	}
	if e := b.Cancel(jobs[0].ID); e == nil {
		t.Fatal("cross-tenant cancel accepted")
	}
	if _, e := b.History(jobs[0].ID); e == nil {
		t.Fatal("cross-tenant history accepted")
	}
	token := remoteToken(t, a)
	code, _ := rawRemote(t, f.http.URL, "PUT", "/v1/settings", token, "", `{"allow_submit":true,"delay_ms":0,"owner":"another"}`)
	if code != 400 {
		t.Fatalf("client owner accepted %d", code)
	}
	code, _ = rawRemote(t, f.http.URL, "GET", "/v1/jobs", "", "", "")
	if code != 401 {
		t.Fatalf("unauthenticated %d", code)
	}
	if len(f.base.seats) != 0 {
		t.Fatal("job creation submitted seats")
	}

	// Execute the stored job with the production scheduler and school protocol,
	// substituting only the school's HTTP transport.
	dir, e := storage.AccountDir(filepath.Join(f.root, "data"), a.Identity().ID)
	if e != nil {
		t.Fatal(e)
	}
	db, e := storage.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	cfg, _, e := config.LoadAccount(filepath.Join(f.root, "data"), a.Identity().ID)
	if e != nil {
		t.Fatal(e)
	}
	due := jobs[0].Next.Add(time.Millisecond)
	e = schedule.Poll(ctx, db, "", due, func(ctx context.Context, j storage.Job) schedule.Outcome {
		login, e := f.login()
		if e != nil {
			t.Fatal(e)
		}
		defer login.Close()
		return schedule.ExecuteWithLogin(ctx, cfg, db, j, login)
	})
	if e != nil {
		t.Fatal(e)
	}
	jobs, e = a.Jobs()
	if e != nil || jobs[0].State != "succeeded" {
		t.Fatalf("execution %v %v", jobs, e)
	}
	if !reflect.DeepEqual(f.base.seats, []string{"002", "001"}) {
		t.Fatal("fallback order", f.base.seats)
	}
	history, e := a.History(jobs[0].ID)
	if e != nil || len(history) != 1 {
		t.Fatalf("history %v %v", history, e)
	}
	if raw, _ := a.Get("last_delay/" + jobs[0].ID); !strings.Contains(string(raw), ":7") {
		t.Fatalf("delay %s", raw)
	}
	if e = schedule.Poll(ctx, db, "", due, func(context.Context, storage.Job) schedule.Outcome {
		t.Fatal("executed twice")
		return schedule.Outcome{}
	}); e != nil {
		t.Fatal(e)
	}

	// Persisted frontend credentials restore without another QR.
	restored, e := api.NewClient(f.http.URL, f.clientRoot)
	if e != nil {
		t.Fatal(e)
	}
	if e = restored.LoadCredentials(a.Identity().ID); e != nil {
		t.Fatal(e)
	}
	if _, e = restored.Me(ctx); e != nil {
		t.Fatal("restore", e)
	}
	// New HTTP handler, same storage, proves server restart restoration.
	f.http.Close()
	f.server.Close()
	f.server, e = apiserver.New(apiserver.Options{Root: f.root, NewLogin: f.login})
	if e != nil {
		t.Fatal(e)
	}
	f.http = httptest.NewServer(f.server)
	code, _ = rawRemote(t, f.http.URL, "GET", "/v1/me", token, "", "")
	if code != 200 {
		t.Fatalf("restart auth %d", code)
	}
	code, _ = rawRemote(t, f.http.URL, "POST", "/v1/logout", token, "", "{}")
	if code != 200 {
		t.Fatal(code)
	}
	code, _ = rawRemote(t, f.http.URL, "GET", "/v1/me", token, "", "")
	if code != 401 {
		t.Fatal("logout did not revoke")
	}
}
func TestRemoteTUICanLoadPeriodsAndSaveAutomaticJob(t *testing.T) {
	f := newRemoteFixture(t)
	c := f.client(t, "20260003")
	cfg, path, e := config.LoadAccount(c.ProfileRoot, c.Identity().ID)
	if e != nil {
		t.Fatal(e)
	}
	m, e := New(cfg, path, WithRemote(c))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(m.Close)
	m.qrWindowLauncher = func(context.Context, [][]bool) error { return context.Canceled }
	m.Update(tea.WindowSizeMsg{Width: 130, Height: 40})
	// Explicit messages avoid running the infinite UI refresh timers.
	feed(t, m, m.cmdOpenHome()())
	if m.session.phase != phaseConfirmed {
		t.Fatalf("home %v", m.session.err)
	}
	day := time.Now().In(schedule.Zone).AddDate(0, 0, 1).Format("2006-01-02")
	feed(t, m, m.cmdListRooms(day)())
	feed(t, m, m.cmdOpenRoom(6299)())
	if len(m.room.slots) == 0 {
		t.Fatalf("remote periods missing: %v", m.room.err)
	}
	m.candidates = []string{"001"}
	plan := make([]storage.DayPlan, 7)
	for i := range plan {
		plan[i] = storage.DayPlan{Enabled: true, Start: "19:00", End: "19:30"}
	}
	j, e := schedule.NewAutomaticJob(6299, m.room.name, m.candidates, day, 0, "daily", plan, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	m.pendingJob = &j
	msg := m.saveScheduledJob()().(jobSavedMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	feed(t, m, m.cmdJobs()())
	if len(m.jobs) != 1 || !m.jobs[0].RepeatForever || m.jobs[0].RepeatMode != "daily" {
		t.Fatalf("automatic job %v", m.jobs)
	}
	if len(f.base.seats) != 0 {
		t.Fatal("TUI preparation wrote to school")
	}
}

func TestRemoteLoginCapabilitiesCancellationAndDeletion(t *testing.T) {
	f := newRemoteFixture(t)
	code, b := rawRemote(t, f.http.URL, "POST", "/v1/login-sessions", "", "", "{}")
	if code != 201 {
		t.Fatalf("create %d %s", code, b)
	}
	var login api.Login
	if e := json.Unmarshal(b, &login); e != nil {
		t.Fatal(e)
	}
	code, _ = rawRemote(t, f.http.URL, "GET", "/v1/login-sessions/"+login.ID, "wrong", "", "")
	if code != 401 {
		t.Fatal("QR capability bypass")
	}
	code, _ = rawRemote(t, f.http.URL, "DELETE", "/v1/login-sessions/"+login.ID, login.Secret, "", "")
	if code != 200 {
		t.Fatal(code)
	}
	code, b = rawRemote(t, f.http.URL, "GET", "/v1/login-sessions/"+login.ID, login.Secret, "", "")
	if code != 200 || !strings.Contains(string(b), "cancelled") || strings.Contains(string(b), `"token"`) {
		t.Fatalf("cancel %d %s", code, b)
	}
	a := f.client(t, "20260004")
	otherSession := f.client(t, "20260004")
	if a.Identity().ID != otherSession.Identity().ID {
		t.Fatal("same identity split")
	}
	token := remoteToken(t, otherSession)
	if e := a.DeleteAccount(); e != nil {
		t.Fatal(e)
	}
	code, _ = rawRemote(t, f.http.URL, "GET", "/v1/me", token, "", "")
	if code != 401 {
		t.Fatal("deletion did not revoke all sessions")
	}
	again := f.client(t, "20260004")
	if jobs, e := again.Jobs(); e != nil || len(jobs) != 0 {
		t.Fatal("deleted jobs resurrected")
	}
	code, _ = rawRemote(t, f.http.URL, "GET", "/v1/me", token, "", "")
	if code != 401 {
		t.Fatal("old session resurrected after new QR")
	}
}
func TestRemoteTUIQRNewAccountAndCancel(t *testing.T) {
	f := newRemoteFixture(t)
	client, e := api.NewClient(f.http.URL, f.clientRoot)
	if e != nil {
		t.Fatal(e)
	}
	cfg, path, e := config.LoadAccount(client.ProfileRoot, "pending-test")
	if e != nil {
		t.Fatal(e)
	}
	m, e := New(cfg, path, WithRemote(client))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(m.Close)
	m.qrWindowLauncher = func(context.Context, [][]bool) error { return context.Canceled }
	m.Update(tea.WindowSizeMsg{Width: 180, Height: 65})
	feed(t, m, m.cmdStartQR()())
	if !validQRModuleCount(len(m.session.qrGrid)) {
		t.Fatalf("QR not visible: %v", m.session.err)
	}
	feed(t, m, m.cmdPollQR()())
	feed(t, m, m.cmdFollowCallback("")())
	feed(t, m, m.cmdOpenHome()())
	if m.session.phase != phaseConfirmed || m.accountLabel() != "张20260001" {
		t.Fatalf("TUI login %v %v", m.session.err, m.accountLabel())
	}
	m.session.busy = false
	m.rooms.loading = false
	m.focusSide(PaneStatus)
	_, nextModel, _ := m.accountControl(keyMsg('n'))
	next := nextModel.(*Model)
	if next == m || next.addAccountReturn != m {
		t.Fatal("new login is not temporary")
	}
	next.qrWindowLauncher = func(context.Context, [][]bool) error { return context.Canceled }
	feed(t, next, next.cmdStartQR()())
	restored, _ := next.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if restored != m || len(m.accountIDs()) != 1 || m.closed {
		t.Fatal("cancel leaked pending account or closed parent")
	}
	m.rooms.loading = false
	m.session.busy = false
	_, nextModel, _ = m.accountControl(keyMsg('n'))
	next = nextModel.(*Model)
	t.Cleanup(next.Close)
	next.qrWindowLauncher = func(context.Context, [][]bool) error { return context.Canceled }
	f.mu.Lock()
	f.student = "20260005"
	f.mu.Unlock()
	feed(t, next, next.cmdStartQR()())
	feed(t, next, next.cmdPollQR()())
	feed(t, next, next.cmdFollowCallback("")())
	feed(t, next, next.cmdOpenHome()())
	if next.addAccountReturn != nil || !m.closed || len(next.accountIDs()) != 2 {
		t.Fatal("confirmed login kept temporary parent")
	}
	if next.accountLabel() != "张20260005" {
		t.Fatal("wrong new account")
	}
}
func TestRemoteConcurrentQueriesKeepDatesAndIdentity(t *testing.T) {
	f := newRemoteFixture(t)
	a := f.client(t, "20260006")
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			day := time.Now().In(schedule.Zone).AddDate(0, 0, 1+i%3).Format("2006-01-02")
			v, e := a.Query(context.Background(), chaoxing.SeatQuery{Operation: "room", Day: day, RoomID: 6299})
			if e != nil {
				errors <- e
				return
			}
			if v.Room == nil || v.Room.Day != day || v.DisplayName != "张20260006" {
				errors <- fmt.Errorf("query crossed context: %+v", v)
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	for e := range errors {
		t.Error(e)
	}
}

type lostJobResponse struct {
	base http.RoundTripper
	lost bool
}

func (f *lostJobResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	response, e := f.base.RoundTrip(r)
	if e == nil && r.Method == "POST" && r.URL.Path == "/v1/jobs" && !f.lost {
		f.lost = true
		response.Body.Close()
		return nil, io.ErrUnexpectedEOF
	}
	return response, e
}
func TestRemoteLostJobResponseRetrySurvivesClientRestart(t *testing.T) {
	f := newRemoteFixture(t)
	c := f.client(t, "20260007")
	c.HTTP = &http.Client{Transport: &lostJobResponse{base: http.DefaultTransport}}
	j := storage.Job{ID: "first-request-key", RoomID: 6299, Day: time.Now().In(schedule.Zone).AddDate(0, 0, 1).Format("2006-01-02"), Start: "19:00", End: "19:30", Seats: []string{"001"}}
	if e := c.Add(j); e == nil {
		t.Fatal("expected lost response")
	}
	restored, e := api.NewClient(f.http.URL, f.clientRoot)
	if e != nil {
		t.Fatal(e)
	}
	if e = restored.LoadCredentials(c.Identity().ID); e != nil {
		t.Fatal(e)
	}
	j.ID = "new-ui-request-key"
	if e = restored.Add(j); e != nil {
		t.Fatal(e)
	}
	jobs, e := restored.Jobs()
	if e != nil || len(jobs) != 1 {
		t.Fatalf("retry duplicated task: %v %v", jobs, e)
	}
}
func TestRemoteExecutorRejectsWrongSchoolIdentity(t *testing.T) {
	f := newRemoteFixture(t)
	c := f.client(t, "20260008")
	root := filepath.Join(f.root, "data")
	cfg, _, e := config.LoadAccount(root, c.Identity().ID)
	if e != nil {
		t.Fatal(e)
	}
	dir, e := storage.AccountDir(root, c.Identity().ID)
	if e != nil {
		t.Fatal(e)
	}
	db, e := storage.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	raw, e := db.Get("cookies")
	if e != nil {
		t.Fatal(e)
	}
	// Simulate credentials accidentally restored into a different owner's store.
	wrong := strings.ReplaceAll(string(raw), "20260008", "20269999")
	if wrong == string(raw) {
		t.Fatal("fixture did not contain identity cookie")
	}
	if e = db.Put("cookies", []byte(wrong)); e != nil {
		t.Fatal(e)
	}
	login, e := f.login()
	if e != nil {
		t.Fatal(e)
	}
	defer login.Close()
	out := schedule.ExecuteWithLogin(context.Background(), cfg, db, storage.Job{ID: "wrong-identity", RoomID: 6299, Day: seatDay, Start: "19:00", End: "19:30", Seats: []string{"001"}}, login)
	if out.State != "needs_login" || len(f.base.seats) != 0 {
		t.Fatalf("wrong owner submitted: %v", out)
	}
}

func assertLatestUA(t *testing.T, f *remoteFixture, expected string) {
	t.Helper()
	f.mu.Lock()
	trace := f.traces[len(f.traces)-1]
	f.mu.Unlock()
	trace.traceMu.Lock()
	defer trace.traceMu.Unlock()
	if len(trace.agents) == 0 {
		t.Fatal("no school requests recorded")
	}
	for _, agent := range trace.agents {
		if agent != expected {
			t.Fatal("account UA changed within school request chain")
		}
	}
}
func TestRemoteAccountUserAgentAcrossLoginQueriesAndExecution(t *testing.T) {
	f := newRemoteFixture(t)
	a := f.client(t, "20260021")
	root := filepath.Join(f.root, "data")
	cfgA, _, e := config.LoadAccount(root, a.Identity().ID)
	if e != nil {
		t.Fatal(e)
	}
	assertLatestUA(t, f, cfgA.UserAgent)
	b := f.client(t, "20260022")
	cfgB, _, e := config.LoadAccount(root, b.Identity().ID)
	if e != nil {
		t.Fatal(e)
	}
	assertLatestUA(t, f, cfgB.UserAgent)
	if cfgA.UserAgent == cfgB.UserAgent || !strings.Contains(cfgA.UserAgent, "WFUseat/") {
		t.Fatal("UA not account-specific")
	}
	if strings.Contains(cfgA.UserAgent, "20260021") {
		t.Fatal("student number leaked into UA")
	}
	if _, e = a.Query(context.Background(), chaoxing.SeatQuery{Operation: "home"}); e != nil {
		t.Fatal(e)
	}
	assertLatestUA(t, f, cfgA.UserAgent)
	restored, e := api.NewClient(f.http.URL, f.clientRoot)
	if e != nil {
		t.Fatal(e)
	}
	if e = restored.LoadCredentials(a.Identity().ID); e != nil {
		t.Fatal(e)
	}
	if _, e = restored.Query(context.Background(), chaoxing.SeatQuery{Operation: "home"}); e != nil {
		t.Fatal(e)
	}
	assertLatestUA(t, f, cfgA.UserAgent)
	f.mu.Lock()
	f.student = "20260021"
	f.mu.Unlock()
	if _, e = a.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	if state, e := a.Poll(context.Background()); e != nil || state != chaoxing.QRConfirmed {
		t.Fatal(state, e)
	}
	assertLatestUA(t, f, cfgA.UserAgent)
	cfgAgain, _, e := config.LoadAccount(root, a.Identity().ID)
	if e != nil || cfgAgain.UserAgent != cfgA.UserAgent {
		t.Fatal("relogin changed UA", e)
	}
	dir, e := storage.AccountDir(root, a.Identity().ID)
	if e != nil {
		t.Fatal(e)
	}
	db, e := storage.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	login, e := f.login()
	if e != nil {
		t.Fatal(e)
	}
	defer login.Close()
	job := storage.Job{ID: "ua-test", RoomID: 6299, Day: seatDay, Start: "19:00", End: "19:30", Seats: []string{"002", "001"}}
	out := schedule.ExecuteWithLogin(context.Background(), cfgA, db, job, login)
	if out.State != "succeeded" {
		t.Fatal(out)
	}
	assertLatestUA(t, f, cfgA.UserAgent)
	// Re-login with a different student's QR must not replace that student's UA.
	f.mu.Lock()
	f.student = "20260022"
	f.mu.Unlock()
	if _, e = a.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e = a.Poll(context.Background()); e == nil {
		t.Fatal("wrong-identity re-login accepted")
	}
	after, _, e := config.LoadAccount(root, b.Identity().ID)
	if e != nil || after.UserAgent != cfgB.UserAgent {
		t.Fatal("another account UA overwritten")
	}
}
