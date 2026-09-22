// Package apiserver owns school credentials, authorization and job creation.
package apiserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"wfuseat/internal/api"
	"wfuseat/internal/chaoxing"
	"wfuseat/internal/config"
	"wfuseat/internal/schedule"
	"wfuseat/internal/storage"
)

type Options struct {
	Root     string
	Proxy    string
	FIDEnc   string
	MappID   string
	NewLogin func() (*chaoxing.QRLogin, error)
	Now      func() time.Time
}
type account struct {
	Identity api.Identity
	Epoch    string
	Deleted  bool
}
type tokenRecord struct {
	Owner   string
	Epoch   string
	Expires time.Time
}
type pending struct {
	expectedOwner string
	mu            sync.Mutex
	login         *chaoxing.QRLogin
	secret        string
	expires       time.Time
	cookieMu      sync.Mutex
	cookies       []byte
	result        api.Login
}
type bucket struct {
	At    time.Time
	Count int
}
type Server struct {
	opts    Options
	auth    *storage.Store
	mu      sync.Mutex
	pending map[string]*pending
	rates   map[string]bucket
	owners  sync.Map
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	slots   chan struct{}
}

func New(o Options) (*Server, error) {
	if o.FIDEnc == "" {
		o.FIDEnc = config.DefaultFIDEnc
	}
	if o.MappID == "" {
		o.MappID = config.DefaultMappID
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Root == "" {
		return nil, errors.New("服务端数据目录不能为空")
	}
	db, e := storage.Open(filepath.Join(o.Root, "auth"))
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{opts: o, auth: db, pending: map[string]*pending{}, rates: map[string]bucket{}, slots: make(chan struct{}, 32), ctx: ctx, cancel: cancel}
	if s.opts.NewLogin == nil {
		s.opts.NewLogin = func() (*chaoxing.QRLogin, error) { return chaoxing.NewQRLogin(o.Proxy, "") }
	}
	s.wg.Add(1)
	go s.expireLoop()
	return s, nil
}
func (s *Server) Close() {
	s.cancel()
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.pending {
		p.mu.Lock()
		if p.login != nil {
			p.login.Close()
		}
		p.mu.Unlock()
	}
	s.auth.Close()
}
func (s *Server) expireLoop() {
	defer s.wg.Done()
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-tick.C:
			s.mu.Lock()
			for id, p := range s.pending {
				if !p.expires.After(s.opts.Now()) {
					delete(s.pending, id)
					p.mu.Lock()
					if p.login != nil {
						p.login.Close()
					}
					p.mu.Unlock()
				}
			}
			for ip, b := range s.rates {
				if s.opts.Now().Sub(b.At) > time.Minute {
					delete(s.rates, ip)
				}
			}
			s.mu.Unlock()
		}
	}
}
func randomID() string {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func hash(v string) string     { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func (s *Server) root() string { return filepath.Join(s.opts.Root, "data") }
func (s *Server) lock(owner string) func() {
	v, _ := s.owners.LoadOrStore(owner, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, msg string) {
	reply(w, status, api.Problem{Error: msg})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 32768)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		problem(w, 400, "请求格式无效")
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		problem(w, 400, "请求包含多余内容")
		return false
	}
	return true
}
func (s *Server) allow(ip string, limit int) bool {
	now := s.opts.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.rates[ip]
	if now.Sub(b.At) >= time.Minute {
		b = bucket{At: now}
	}
	b.Count++
	if len(s.rates) > 4096 {
		return false
	}
	s.rates[ip] = b
	return b.Count <= limit
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !s.allow("ip/"+ip, 6000) {
		problem(w, 429, "请求过于频繁，请稍后重试")
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		problem(w, 503, "服务繁忙，请稍后重试")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	path := r.URL.Path
	if path == "/v1/health" && r.Method == "GET" {
		reply(w, 200, map[string]string{"version": api.Version})
		return
	}
	if path == "/v1/login-sessions" && r.Method == "POST" {
		if !s.allow("qr/"+ip, 20) {
			problem(w, 429, "二维码请求过于频繁，请稍后重试")
			return
		}
		s.startLogin(w, r)
		return
	}
	if strings.HasPrefix(path, "/v1/login-sessions/") {
		s.pollLogin(w, r, strings.TrimPrefix(path, "/v1/login-sessions/"))
		return
	}
	a, e := s.authenticate(r)
	if e != nil {
		problem(w, 401, "会话已失效，请扫码登录")
		return
	}
	if !s.allow("owner/"+a.Identity.ID, 180) {
		problem(w, 429, "请求过于频繁，请稍后重试")
		return
	}
	unlock := s.lock(a.Identity.ID)
	defer unlock()
	// Recheck after acquiring the account lock, including concurrent deletion.
	a, e = s.authenticate(r)
	if e != nil {
		problem(w, 401, "会话已失效，请扫码登录")
		return
	}
	cfg, _, e := config.LoadAccount(s.root(), a.Identity.ID)
	if e != nil {
		problem(w, 500, "账号配置不可用")
		return
	}
	dir, _ := storage.AccountDir(s.root(), a.Identity.ID)
	db, e := storage.Open(dir)
	if e != nil {
		problem(w, 500, "账号存储不可用")
		return
	}
	defer db.Close()
	switch {
	case path == "/v1/me" && r.Method == "GET":
		worker, _ := db.Get("worker_error")
		note := ""
		if len(worker) > 0 {
			note = "后端调度暂时暂停，请检查登录状态"
		}
		reply(w, 200, api.Me{Identity: a.Identity, Settings: api.Settings{AllowSubmit: cfg.AllowSubmit && db.Enabled(), DelayMS: cfg.DelayMS}, WorkerError: note})
	case path == "/v1/settings" && r.Method == "PUT":
		var v api.Settings
		if !decode(w, r, &v) {
			return
		}
		if v.DelayMS < 0 || v.DelayMS > 60000 {
			problem(w, 400, "延迟需要 0–60000 毫秒")
			return
		}
		cfg.DelayMS = v.DelayMS
		cfg.AllowSubmit = v.AllowSubmit
		if e = db.SetEnabled(v.AllowSubmit); e == nil {
			_, e = config.Save(cfg)
		}
		if e != nil {
			problem(w, 500, "配置保存失败")
			return
		}
		reply(w, 200, v)
	case path == "/v1/query" && r.Method == "POST":
		var q chaoxing.SeatQuery
		if !decode(w, r, &q) {
			return
		}
		s.query(w, r, a, cfg, db, q)
	case path == "/v1/jobs" && r.Method == "GET":
		jobs, e := db.Jobs()
		if e != nil {
			problem(w, 500, "任务读取失败")
			return
		}
		delays := map[string]int{}
		for _, j := range jobs {
			b, _ := db.Get("last_delay/" + j.ID)
			var v struct {
				Delay int `json:"delay_ms"`
			}
			if json.Unmarshal(b, &v) == nil {
				delays[j.ID] = v.Delay
			}
		}
		reply(w, 200, api.Jobs{Items: jobs, Delays: delays})
	case path == "/v1/jobs" && r.Method == "POST":
		s.createJob(w, r, a, cfg, db)
	case strings.HasPrefix(path, "/v1/jobs/"):
		id := strings.TrimPrefix(path, "/v1/jobs/")
		history := strings.HasSuffix(id, "/runs")
		if history {
			id = strings.TrimSuffix(id, "/runs")
		}
		jobs, _ := db.Jobs()
		found := false
		for _, j := range jobs {
			if j.ID == id {
				found = true
				break
			}
		}
		if !found {
			problem(w, 404, "任务不存在")
			return
		}
		if history && r.Method == "GET" {
			runs, e := db.History(id)
			if e != nil {
				problem(w, 500, "记录读取失败")
				return
			}
			reply(w, 200, runs)
			return
		}
		if !history && r.Method == "DELETE" {
			if e = db.Cancel(id); e != nil {
				problem(w, 409, "任务正在执行，不能取消")
				return
			}
			reply(w, 200, map[string]bool{"ok": true})
			return
		}
		problem(w, 405, "不支持的操作")
	case path == "/v1/logout" && r.Method == "POST":
		if e = s.auth.Delete("token/" + hash(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))); e != nil {
			problem(w, 500, "退出失败，请重试")
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	case path == "/v1/me" && r.Method == "DELETE":
		db.Close()
		if e = storage.RemoveAccount(s.root(), a.Identity.ID); e != nil {
			problem(w, 409, "账号仍有执行中的任务")
			return
		}
		a.Deleted = true
		a.Epoch = randomID()
		b, _ := json.Marshal(a)
		if e = s.auth.Put("account/"+a.Identity.ID, b); e != nil {
			problem(w, 500, "会话撤销失败，请重试")
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	default:
		problem(w, 404, "接口不存在")
	}
}
func (s *Server) authenticate(r *http.Request) (account, error) {
	var a account
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return a, errors.New("token")
	}
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(raw) != 64 {
		return a, errors.New("token")
	}
	b, e := s.auth.Get("token/" + hash(raw))
	var tr tokenRecord
	if e != nil || json.Unmarshal(b, &tr) != nil || !tr.Expires.After(s.opts.Now()) {
		return a, errors.New("expired")
	}
	b, e = s.auth.Get("account/" + tr.Owner)
	if e != nil || json.Unmarshal(b, &a) != nil || a.Deleted || a.Epoch != tr.Epoch {
		return a, errors.New("account")
	}
	return a, nil
}
func (s *Server) startLogin(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if len(s.pending) >= 64 {
		s.mu.Unlock()
		problem(w, 429, "登录请求已满，请稍后重试")
		return
	}
	id, secret := randomID(), randomID()
	p := &pending{secret: hash(secret), expires: s.opts.Now().Add(3 * time.Minute)}
	s.pending[id] = p
	p.mu.Lock()
	s.mu.Unlock()
	defer p.mu.Unlock()
	l, e := s.opts.NewLogin()
	if e != nil {
		p.result.State = "failed"
		problem(w, 502, "无法创建学校登录")
		return
	}
	l.Session.UserAgent = config.AccountUserAgent(id)
	// Authenticated re-login keeps this account's existing browser identity.
	if a, err := s.authenticate(r); err == nil {
		p.expectedOwner = a.Identity.ID
		unlock := s.lock(a.Identity.ID)
		cfg, _, err := config.LoadAccount(s.root(), a.Identity.ID)
		unlock()
		if err == nil && cfg.UserAgent != "" {
			l.Session.UserAgent = cfg.UserAgent
		}
	}
	p.login = l
	e = l.Session.AttachCookies(nil, func(b []byte) error {
		p.cookieMu.Lock()
		p.cookies = append([]byte(nil), b...)
		p.cookieMu.Unlock()
		return nil
	})
	var png []byte
	if e == nil {
		png, e = l.Start(r.Context())
	}
	if e != nil {
		p.result.State = "failed"
		problem(w, 502, qrFailure(e, s.opts.Proxy))
		return
	}
	p.result = api.Login{ID: id, PNG: png, State: "waiting", Expires: p.expires}
	v := p.result
	v.Secret = secret
	reply(w, 201, v)
}
func (s *Server) pollLogin(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	p := s.pending[id]
	s.mu.Unlock()
	if p == nil {
		problem(w, 404, "登录已取消或过期")
		return
	}
	secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(hash(secret)), []byte(p.secret)) != 1 {
		problem(w, 401, "登录凭据无效")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.Method == "DELETE" {
		p.result = api.Login{ID: id, State: "cancelled", Expires: p.expires}
		if p.login != nil {
			p.login.Close()
		}
		reply(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.Method != "GET" {
		problem(w, 405, "不支持的操作")
		return
	}
	if !p.expires.After(s.opts.Now()) {
		problem(w, 410, "二维码已过期")
		return
	}
	if p.result.State != "waiting" {
		v := p.result
		v.PNG = nil
		reply(w, 200, v)
		return
	}
	state, target, e := p.login.PollOnce(r.Context())
	if e != nil {
		problem(w, 502, "扫码状态查询失败")
		return
	}
	if state == chaoxing.QRExpired {
		p.result.State = "expired"
	} else if state == chaoxing.QRConfirmed {
		p.result.State = "failed" // Never reuse an authorization code on an ambiguous callback.
		if _, e = p.login.FollowCallback(r.Context(), target); e != nil {
			problem(w, 502, "学校登录回调失败，请重新扫码")
			return
		}
		c := chaoxing.NewSeatClient(p.login, s.opts.FIDEnc, s.opts.MappID)
		if _, e = c.OpenHome(r.Context()); e != nil {
			problem(w, 502, "学校身份校验失败")
			return
		}
		student, label := c.StudentID(), c.AccountName()
		if student == "" || label == "" {
			problem(w, 403, "未取得可验证的学校身份")
			return
		}
		owner := storage.ServiceAccountID(s.opts.FIDEnc, student)
		if p.expectedOwner != "" && p.expectedOwner != owner {
			problem(w, 403, "扫码身份与当前账号不一致，请新增账号")
			return
		}
		unlock := s.lock(owner)
		defer unlock()
		identity := api.Identity{ID: owner, DisplayName: label, School: s.opts.FIDEnc, Student: student}
		var a account
		b, _ := s.auth.Get("account/" + owner)
		json.Unmarshal(b, &a)
		if a.Epoch == "" || a.Deleted {
			a = account{Identity: identity, Epoch: randomID()}
		}
		a.Identity = identity
		a.Deleted = false
		cfg, _, e := config.LoadAccount(s.root(), owner)
		if e != nil {
			problem(w, 500, "账号初始化失败")
			return
		}
		cfg.UserAgent = p.login.Session.UserAgent
		cfg.Proxy = s.opts.Proxy
		cfg.FIDEnc = s.opts.FIDEnc
		cfg.MappID = s.opts.MappID
		if _, e = config.Save(cfg); e != nil {
			problem(w, 500, "账号配置保存失败")
			return
		}
		dir, _ := storage.AccountDir(s.root(), owner)
		db, e := storage.Open(dir)
		if e != nil {
			problem(w, 500, "凭证存储不可用")
			return
		}
		defer db.Close()
		p.cookieMu.Lock()
		cookies := append([]byte(nil), p.cookies...)
		p.cookieMu.Unlock()
		if len(cookies) == 0 {
			problem(w, 502, "学校会话未返回可保存的凭证")
			return
		}
		if e = db.Put("cookies", cookies); e != nil {
			problem(w, 500, "凭证保存失败")
			return
		}
		b, _ = json.Marshal(a)
		if e = s.auth.Put("account/"+owner, b); e != nil {
			problem(w, 500, "身份保存失败")
			return
		}
		token := randomID()
		expires := s.opts.Now().Add(30 * 24 * time.Hour)
		b, _ = json.Marshal(tokenRecord{Owner: owner, Epoch: a.Epoch, Expires: expires})
		if e = s.auth.Put("token/"+hash(token), b); e != nil {
			problem(w, 500, "会话保存失败")
			return
		}
		p.result = api.Login{ID: id, State: "confirmed", Token: token, Identity: identity, Expires: expires}
	}
	v := p.result
	v.PNG = nil
	reply(w, 200, v)
}
func (s *Server) school(cfg config.Config, db *storage.Store) (*chaoxing.SeatClient, func(), error) {
	l, e := s.opts.NewLogin()
	if e != nil {
		return nil, func() {}, e
	}
	cfg.Normalize()
	if cfg.UserAgent != "" {
		l.Session.UserAgent = cfg.UserAgent
	}
	b, e := db.Get("cookies")
	if e == nil && len(b) > 0 {
		e = l.Session.AttachCookies(b, func(b []byte) error { return db.Put("cookies", b) })
	} else {
		e = errors.New("missing cookies")
	}
	if e != nil {
		l.Close()
		return nil, func() {}, e
	}
	c := chaoxing.NewSeatClient(l, cfg.FIDEnc, cfg.MappID)
	return c, func() { l.Close() }, nil
}
func (s *Server) query(w http.ResponseWriter, r *http.Request, a account, cfg config.Config, db *storage.Store, q chaoxing.SeatQuery) {
	if q.Operation != "home" && q.Operation != "rooms" && q.Operation != "room" && q.Operation != "seats" {
		problem(w, 400, "查询类型无效")
		return
	}
	if q.Operation != "home" {
		if _, e := time.Parse("2006-01-02", q.Day); e != nil {
			problem(w, 400, "日期无效")
			return
		}
	}
	c, close, e := s.school(cfg, db)
	if e != nil {
		problem(w, 401, "学校凭证不可用，请重新扫码")
		return
	}
	defer close()
	home, e := c.OpenHome(r.Context())
	if e != nil {
		problem(w, 502, "学校登录校验失败，请重新扫码")
		return
	}
	if c.StudentID() != a.Identity.Student {
		problem(w, 403, "学校身份不匹配")
		return
	}
	view := c.PublicView()
	view.Home = home
	if q.Operation != "home" {
		rooms, e := c.ListRooms(r.Context(), q.Day)
		if e != nil {
			problem(w, 502, "教室查询失败")
			return
		}
		view.Rooms = rooms
		view.Home = nil
	}
	if q.Operation == "room" || q.Operation == "seats" {
		info, e := c.OpenRoom(r.Context(), q.RoomID)
		if e != nil {
			problem(w, 502, "时段查询失败")
			return
		}
		view = c.PublicView()
		view.Room = info
		if q.Operation == "seats" {
			view.Occupancy, e = c.SeatMap(r.Context(), q.Start, q.End)
			if e != nil {
				problem(w, 502, "座位查询失败")
				return
			}
		}
	}
	reply(w, 200, view)
}
func (s *Server) createJob(w http.ResponseWriter, r *http.Request, a account, cfg config.Config, db *storage.Store) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 128 {
		problem(w, 400, "需要 8–128 字符的 Idempotency-Key")
		return
	}
	var v api.JobDraft
	if !decode(w, r, &v) {
		return
	}
	raw, _ := json.Marshal(v)
	digest := hash(string(raw))
	// Fast durable replay; AddOnce still protects concurrent/restarted handlers.
	if b, _ := db.Get("request/" + key); len(b) > 0 {
		var old struct {
			Digest string
			Job    storage.Job
		}
		if json.Unmarshal(b, &old) != nil || old.Digest != digest {
			problem(w, 409, "幂等键已用于不同请求")
			return
		}
		reply(w, 200, old.Job)
		return
	}
	if !cfg.AllowSubmit || !db.Enabled() {
		problem(w, 409, "账号预订已暂停")
		return
	}
	jobs, _ := db.Jobs()
	active := 0
	for _, j := range jobs {
		if j.State == "pending" || j.State == "running" {
			active++
		}
	}
	if active >= 100 {
		problem(w, 409, "待执行任务已达上限")
		return
	}
	var j storage.Job
	var e error
	now := s.opts.Now()
	if v.Mode != "" {
		j, e = schedule.NewAutomaticJob(v.RoomID, "", v.Seats, v.Day, 0, v.Mode, v.WeekPlan, now)
	} else {
		j, e = schedule.NewJob(v.RoomID, "", v.Day, v.Start, v.End, v.Seats, "", now.Add(2*time.Second).In(schedule.Zone).Format("15:04:05"), now)
	}
	if e != nil {
		problem(w, 400, e.Error())
		return
	}
	c, close, e := s.school(cfg, db)
	if e != nil {
		problem(w, 401, "请重新扫码")
		return
	}
	defer close()
	if _, e = c.OpenHome(r.Context()); e != nil {
		problem(w, 502, "学校登录校验失败")
		return
	}
	if c.StudentID() != a.Identity.Student {
		problem(w, 403, "学校身份不匹配")
		return
	}
	reference := j.Day
	if reference > now.In(schedule.Zone).AddDate(0, 0, 6).Format("2006-01-02") {
		reference = now.In(schedule.Zone).AddDate(0, 0, 1).Format("2006-01-02")
	}
	if _, e = c.ListRooms(r.Context(), reference); e != nil {
		problem(w, 502, "教室查询失败")
		return
	}
	info, e := c.OpenRoom(r.Context(), j.RoomID)
	if e != nil {
		problem(w, 502, "教室验证失败")
		return
	}
	if e = c.ValidatePlan(j.Seats, j.Start, j.End); e != nil {
		problem(w, 400, "座位或时间区间不属于该教室")
		return
	}
	j.Room = info.Room
	j.ID = randomID()
	if v.Mode == "" {
		status, at, ok := c.ReserveWindow()
		if !ok || (status != "AVAILABLE" && status != "BEFORE_OPEN") || (status == "BEFORE_OPEN" && at.IsZero()) {
			problem(w, 409, "尚未取得预约开放时间")
			return
		}
		if status == "AVAILABLE" {
			at = now.Add(2 * time.Second)
		}
		j.Next = at
		j.RoomWindow = true
		j.GlobalTime = ""
	}
	result, e := db.AddOnce(key, digest, j)
	if e != nil {
		problem(w, 409, "任务保存失败，请使用相同幂等键重试")
		return
	}
	reply(w, 201, result)
}

// Run serves HTTP behind a TLS reverse proxy or directly with a TLS certificate.
func (s *Server) Run(ctx context.Context, addr, cert, key string) error {
	h := &http.Server{Addr: addr, Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 100 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	done := make(chan error, 1)
	go func() {
		if cert != "" {
			done <- h.ListenAndServeTLS(cert, key)
		} else {
			done <- h.ListenAndServe()
		}
	}()
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); schedule.Serve(workerCtx, s.root()) }()
	select {
	case <-ctx.Done():
		stop, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		h.Shutdown(stop)
		cancel()
		<-workerDone
		return nil
	case e := <-done:
		cancel()
		<-workerDone
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	}
}
