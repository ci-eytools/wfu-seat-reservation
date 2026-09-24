package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"wfuseat/internal/chaoxing"
	"wfuseat/internal/storage"
)

type Credentials struct {
	Server   string   `json:"server"`
	Token    string   `json:"token"`
	Identity Identity `json:"identity"`
}
type Client struct {
	BaseURL              string
	ProfileRoot          string
	HTTP                 *http.Client
	mu                   sync.RWMutex
	submitMu             sync.Mutex
	credentials          Credentials
	loginID, loginSecret string
	loginGeneration      uint64
	cacheMe              Me
	cacheJobs            Jobs
}

func NormalizeEndpoint(raw string) (string, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("服务端地址格式无效")
	}
	h := u.Hostname()
	if u.Scheme != "https" && !(u.Scheme == "http" && (h == "localhost" || h == "127.0.0.1" || h == "::1")) {
		return "", errors.New("远程服务必须使用 HTTPS；HTTP 仅限本机")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}
func NewClient(endpoint, root string) (*Client, error) {
	base, e := NormalizeEndpoint(endpoint)
	if e != nil {
		return nil, e
	}
	h := sha256.Sum256([]byte(base))
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Remote connections never inherit environment proxies.
	return &Client{BaseURL: base, ProfileRoot: filepath.Join(root, "remotes", hex.EncodeToString(h[:16])), HTTP: &http.Client{Transport: transport, Timeout: 95 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Fresh() *Client {
	return &Client{BaseURL: c.BaseURL, ProfileRoot: c.ProfileRoot, HTTP: c.HTTP}
}
func (c *Client) Identity() Identity {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.credentials.Identity
}
func (c *Client) Authorized() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.credentials.Token != ""
}
func (c *Client) token() string { c.mu.RLock(); defer c.mu.RUnlock(); return c.credentials.Token }
func (c *Client) call(ctx context.Context, method, path, token, key string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, e := json.Marshal(in)
		if e != nil {
			return e
		}
		body = bytes.NewReader(b)
	}
	r, e := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if e != nil {
		return e
	}
	r.Header.Set("Accept", "application/json")
	if in != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	resp, e := c.HTTP.Do(r)
	if e != nil {
		if errors.Is(e, context.Canceled) {
			return context.Canceled
		}
		return errors.New("无法连接服务端，请检查地址和网络；若已开启 VPN、TUN 或系统代理，请尝试关闭后重试")
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if e != nil {
		return e
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var p Problem
		json.Unmarshal(b, &p)
		if p.Error == "" {
			p.Error = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("服务端 %d：%s", resp.StatusCode, p.Error)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}
func (c *Client) Start(ctx context.Context) ([]byte, error) {
	c.CancelLogin()
	c.mu.Lock()
	c.loginGeneration++
	generation := c.loginGeneration
	c.mu.Unlock()
	var v Login
	if e := c.call(ctx, "POST", "/v1/login-sessions", c.token(), "", struct{}{}, &v); e != nil {
		return nil, e
	}
	c.mu.Lock()
	if generation != c.loginGeneration {
		c.mu.Unlock()
		c.cancelPending(v.ID, v.Secret)
		return nil, context.Canceled
	}
	c.loginID = v.ID
	c.loginSecret = v.Secret
	c.mu.Unlock()
	return v.PNG, nil
}
func (c *Client) Poll(ctx context.Context) (chaoxing.QRState, error) {
	c.mu.RLock()
	id, secret, expected, generation := c.loginID, c.loginSecret, c.credentials.Identity.ID, c.loginGeneration
	c.mu.RUnlock()
	if id == "" {
		return chaoxing.QRExpired, nil
	}
	var v Login
	if e := c.call(ctx, "GET", "/v1/login-sessions/"+url.PathEscape(id), secret, "", nil, &v); e != nil {
		return "", e
	}
	switch v.State {
	case "confirmed":
		if !storage.IsServiceAccount(v.Identity.ID) || v.Token == "" {
			return "", errors.New("服务端未返回有效身份")
		}
		if expected != "" && expected != v.Identity.ID {
			c.call(ctx, "POST", "/v1/logout", v.Token, "", struct{}{}, nil)
			return "", errors.New("扫码身份与当前账号不一致，请新增账号")
		}
		c.mu.Lock()
		if generation != c.loginGeneration {
			c.mu.Unlock()
			c.call(ctx, "POST", "/v1/logout", v.Token, "", struct{}{}, nil)
			return "", context.Canceled
		}
		c.credentials = Credentials{c.BaseURL, v.Token, v.Identity}
		c.mu.Unlock()
		if e := c.SaveCredentials(); e != nil {
			return "", e
		}
		return chaoxing.QRConfirmed, nil
	case "expired", "cancelled", "failed":
		return chaoxing.QRExpired, nil
	default:
		return chaoxing.QRWaiting, nil
	}
}
func (c *Client) detachLogin() (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, secret := c.loginID, c.loginSecret
	c.loginGeneration++
	c.loginID, c.loginSecret = "", ""
	return id, secret
}
func (c *Client) CancelLogin() {
	id, secret := c.detachLogin()
	c.cancelPending(id, secret)
}

// CancelLoginAsync invalidates the current QR synchronously, then releases its
// server capability without blocking the terminal event loop.
func (c *Client) CancelLoginAsync() {
	id, secret := c.detachLogin()
	if id != "" {
		go c.cancelPending(id, secret)
	}
}
func (c *Client) cancelPending(id, secret string) {
	if id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.call(ctx, "DELETE", "/v1/login-sessions/"+url.PathEscape(id), secret, "", nil, nil)
}
func (c *Client) SaveCredentials() error {
	c.mu.RLock()
	v := c.credentials
	c.mu.RUnlock()
	if !storage.IsServiceAccount(v.Identity.ID) {
		return errors.New("身份无效")
	}
	dir, e := storage.AccountDir(c.ProfileRoot, v.Identity.ID)
	if e != nil {
		return e
	}
	b, _ := json.Marshal(v)
	f, e := os.CreateTemp(dir, ".session-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(name, filepath.Join(dir, "session.json"))
}
func (c *Client) LoadCredentials(id string) error {
	if !storage.IsServiceAccount(id) {
		return errors.New("远程身份无效")
	}
	b, e := os.ReadFile(filepath.Join(c.ProfileRoot, "accounts", id, "session.json"))
	if e != nil {
		return e
	}
	var v Credentials
	if e = json.Unmarshal(b, &v); e != nil {
		return e
	}
	if v.Server != c.BaseURL || v.Identity.ID != id {
		return errors.New("会话不属于此服务端")
	}
	c.mu.Lock()
	c.credentials = v
	c.mu.Unlock()
	return nil
}
func (c *Client) Profiles() map[string]string {
	out := map[string]string{}
	ids, _ := storage.Accounts(c.ProfileRoot)
	for _, id := range ids {
		if !storage.IsServiceAccount(id) {
			continue
		}
		b, e := os.ReadFile(filepath.Join(c.ProfileRoot, "accounts", id, "session.json"))
		if e != nil {
			continue
		}
		var v Credentials
		if json.Unmarshal(b, &v) == nil && v.Server == c.BaseURL {
			out[id] = v.Identity.DisplayName
		}
	}
	return out
}
func (c *Client) Me(ctx context.Context) (Me, error) {
	var v Me
	e := c.call(ctx, "GET", "/v1/me", c.token(), "", nil, &v)
	if e == nil {
		c.mu.Lock()
		c.cacheMe = v
		c.mu.Unlock()
	}
	return v, e
}
func (c *Client) Settings(ctx context.Context, v Settings) error {
	if e := c.call(ctx, "PUT", "/v1/settings", c.token(), "", v, nil); e != nil {
		return e
	}
	c.mu.Lock()
	c.cacheMe.Settings = v
	c.mu.Unlock()
	return nil
}
func (c *Client) Query(ctx context.Context, q chaoxing.SeatQuery) (*chaoxing.SeatView, error) {
	if q.Operation == "home" {
		if _, e := c.Me(ctx); e != nil {
			return nil, e
		}
	}
	var v chaoxing.SeatView
	e := c.call(ctx, "POST", "/v1/query", c.token(), "", q, &v)
	return &v, e
}
func (c *Client) Jobs() ([]storage.Job, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var v Jobs
	e := c.call(ctx, "GET", "/v1/jobs", c.token(), "", nil, &v)
	if e == nil {
		c.mu.Lock()
		c.cacheJobs = v
		c.mu.Unlock()
	}
	return v.Items, e
}
func (c *Client) Add(j storage.Job) error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	v := JobDraft{RoomID: j.RoomID, Day: j.Day, Start: j.Start, End: j.End, Seats: j.Seats}
	if j.RepeatForever || j.RepeatUntil != "" {
		v.Mode = j.RepeatMode
		v.Day = j.RepeatStart
		v.WeekPlan = j.WeekPlan
	}
	// Keep a request key across ambiguous failures, UI retries and restarts.
	// The marker contains no cookie or bearer token.
	c.submitMu.Lock()
	defer c.submitMu.Unlock()
	dir, e := storage.AccountDir(c.ProfileRoot, c.Identity().ID)
	if e != nil {
		return e
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	digest := sha256.Sum256(raw)
	marker := filepath.Join(dir, fmt.Sprintf("request-%x.pending", digest))
	key := j.ID
	file, e := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(e, os.ErrExist) {
		saved, err := os.ReadFile(marker)
		if err != nil {
			return err
		}
		key = string(saved)
	} else if e != nil {
		return e
	} else {
		_, e = file.WriteString(key)
		if e == nil {
			e = file.Sync()
		}
		closeErr := file.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if len(key) < 8 || len(key) > 128 {
		return errors.New("任务请求标识尚未完整保存，请重试")
	}
	if e = c.call(ctx, "POST", "/v1/jobs", c.token(), key, v, nil); e != nil {
		return e
	}
	// A retained marker is harmless: replaying it returns the original job.
	if saved, err := os.ReadFile(marker); err == nil && string(saved) == key {
		_ = os.Remove(marker)
	}
	return nil
}
func (c *Client) Cancel(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return c.call(ctx, "DELETE", "/v1/jobs/"+url.PathEscape(id), c.token(), "", nil, nil)
}
func (c *Client) DeleteAccount() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if e := c.call(ctx, "DELETE", "/v1/me", c.token(), "", nil, nil); e != nil {
		return e
	}
	id := c.Identity().ID
	return os.Remove(filepath.Join(c.ProfileRoot, "accounts", id, "session.json"))
}
func (c *Client) History(id string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var v []string
	e := c.call(ctx, "GET", "/v1/jobs/"+url.PathEscape(id)+"/runs", c.token(), "", nil, &v)
	return v, e
}
func (c *Client) Get(key string) ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if key == "worker_error" {
		return []byte(c.cacheMe.WorkerError), nil
	}
	if strings.HasPrefix(key, "last_delay/") {
		id := strings.TrimPrefix(key, "last_delay/")
		if delay, ok := c.cacheJobs.Delays[id]; ok {
			return json.Marshal(map[string]int{"delay_ms": delay})
		}
	}
	return nil, nil
}
func (c *Client) Put(string, []byte) error {
	return errors.New("客户端不能写入服务端私有凭证")
}
func (c *Client) Enabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cacheMe.Settings.AllowSubmit
}
func (c *Client) SetEnabled(enabled bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c.mu.RLock()
	v := c.cacheMe.Settings
	c.mu.RUnlock()
	v.AllowSubmit = enabled
	return c.Settings(ctx, v)
}
func (c *Client) Close() error { return nil }
func (c *Client) CachedMe() Me { c.mu.RLock(); defer c.mu.RUnlock(); return c.cacheMe }
