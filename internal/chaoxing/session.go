package chaoxing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/net/publicsuffix"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout matches the per-request timeout used by the Python client.
const DefaultTimeout = 30 * time.Second

// maxBodyBytes bounds how much of a response body is ever buffered.
const maxBodyBytes = 8 << 20

func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, limit))
}

func restoreBody(resp *http.Response, body []byte) {
	resp.Body = io.NopCloser(bytes.NewReader(body))
}

// Response is a fully buffered HTTP response, the counterpart of a requests
// Response that has already been loaded.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	URL        *url.URL
}

// Text returns the body as a UTF-8 string. All observed endpoints in this
// project serve UTF-8; the TUI surfaces a decode error rather than mojibake if
// that ever changes.
func (r *Response) Text() string { return string(r.Body) }

// Decode unmarshals the body as JSON.
func (r *Response) Decode(v any) error { return json.Unmarshal(r.Body, v) }

var redirectStatuses = map[int]bool{301: true, 302: true, 303: true, 307: true, 308: true}

// IsRedirect mirrors requests' Response.is_redirect.
func (r *Response) IsRedirect() bool {
	return r.Header.Get("Location") != "" && redirectStatuses[r.StatusCode]
}

// Location returns the raw Location header.
func (r *Response) Location() string { return r.Header.Get("Location") }

// CookieNames returns the names of cookies set by this response.
func (r *Response) CookieNames() []string {
	names := []string{}
	for _, c := range (&http.Response{Header: r.Header}).Cookies() {
		names = append(names, c.Name)
	}
	return sortedUnique(names)
}

// SessionConfig configures a guarded session.
type SessionConfig struct {
	// Proxy, when non-empty, is used for all requests. An empty value means a
	// direct connection: environment proxies are deliberately ignored, matching
	// the Python client's trust_env = False.
	Proxy string
	// LogDir enables the redacted request log when non-empty.
	LogDir string
	// BaseTransport overrides the underlying transport. Nil means a default
	// HTTP transport. It is used by tests and by callers needing custom TLS.
	BaseTransport http.RoundTripper
}

// NewSession builds an HTTP session with the read-only guard installed.
func NewSession(proxy, logDir string) (*Session, error) {
	return NewSessionWithConfig(SessionConfig{Proxy: proxy, LogDir: logDir})
}

// NewSessionWithConfig builds a session with explicit configuration.
func NewSessionWithConfig(cfg SessionConfig) (*Session, error) {
	guard := NewGuardedTransport(cfg.BaseTransport)
	if cfg.BaseTransport == nil {
		transport := &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          20,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
		if cfg.Proxy != "" {
			u, err := url.Parse(cfg.Proxy)
			if err != nil {
				return nil, fmt.Errorf("代理地址无效: %w", err)
			}
			transport.Proxy = http.ProxyURL(u)
		}
		guard = NewGuardedTransport(transport)
	}

	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, err
	}

	s := &Session{
		Proxy: cfg.Proxy,
		Guard: guard,
		jar:   jar,
	}
	if cfg.LogDir != "" {
		log, err := NewLoginLog(cfg.LogDir)
		if err != nil {
			return nil, err
		}
		log.ProxyConfigured = cfg.Proxy != ""
		log.TrustEnv = false
		guard.SetLog(log)
		s.Log = log
	}

	s.Client = &http.Client{
		Transport: guard,
		Jar:       jar,
		// Every call site in the original client used allow_redirects=False and
		// followed redirects explicitly, validating each hop. Keep that: the
		// guard still sees each hop we issue ourselves.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return s, nil
}

// Session owns the guarded HTTP client and cookie jar.
type Session struct {
	Client *http.Client
	Guard  *GuardedTransport
	Log    *LoginLog
	Proxy  string

	// UserAgent, when set, is sent with every request. The WFU callback rejects
	// the default Go user agent, exactly as it rejected the default requests UA.
	UserAgent string

	jar http.CookieJar
}

// Close releases resources held by the session.
func (s *Session) Close() error {
	if s.Log != nil {
		return s.Log.Close()
	}
	return nil
}

// BlockedSeatRequests reports how many requests the read-only policy refused.
func (s *Session) BlockedSeatRequests() int64 { return s.Guard.BlockedCount() }

// CookieNames returns the names of cookies currently held for the given host.
func (s *Session) CookieNames(rawURL string) []string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	names := []string{}
	for _, c := range s.jar.Cookies(u) {
		names = append(names, c.Name)
	}
	return sortedUnique(names)
}

// Get performs a GET, appending params to any existing query string exactly like
// requests' params= argument.
func (s *Session) Get(ctx context.Context, rawURL string, params url.Values) (*Response, error) {
	return s.do(ctx, http.MethodGet, mergeQuery(rawURL, params), nil, nil)
}

// PostForm performs a form-encoded POST, the counterpart of requests' data=.
func (s *Session) PostForm(ctx context.Context, rawURL string, params url.Values) (*Response, error) {
	return s.do(ctx, http.MethodPost, rawURL, params, nil)
}

// do executes one request with the guard in place and buffers the response.
func (s *Session) do(ctx context.Context, method, rawURL string, form url.Values, header http.Header) (*Response, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if s.UserAgent != "" {
		req.Header.Set("User-Agent", s.UserAgent)
	}
	for name, values := range header {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	buf, err := readAllLimited(resp.Body, maxBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	return &Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       buf,
		URL:        resp.Request.URL,
	}, nil
}

// mergeQuery appends params to the URL's existing query string.
func mergeQuery(rawURL string, params url.Values) string {
	if len(params) == 0 {
		return rawURL
	}
	return mergeQueryString(rawURL, params.Encode())
}

// mergeQueryString appends an already-encoded query string. It exists so callers
// that must preserve parameter order (the WFU callback login) can do so.
func mergeQueryString(rawURL, encoded string) string {
	if encoded == "" {
		return rawURL
	}
	if strings.Contains(rawURL, "?") {
		return rawURL + "&" + encoded
	}
	return rawURL + "?" + encoded
}

// resolveURL resolves a (possibly relative) Location header against base, the
// counterpart of urllib.parse.urljoin.
func resolveURL(base, location string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(location)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(ref).String(), nil
}
