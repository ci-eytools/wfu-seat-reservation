package chaoxing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultLogDir is where redacted request logs are written. The original Python
// module used a path relative to the repository root; the Go client resolves it
// relative to the process working directory and the TUI can override it.
const DefaultLogDir = "work/login_logs"

// safeURL mirrors login_diagnostics.safe_url: it keeps scheme/host/path and
// replaces every query value with [REDACTED].
func safeURL(value string) string {
	p, err := url.Parse(value)
	if err != nil {
		return "[REDACTED]"
	}
	out := url.URL{Scheme: p.Scheme, Host: p.Host, Path: p.Path}
	if p.RawQuery != "" {
		values, err := url.ParseQuery(p.RawQuery)
		if err != nil {
			out.RawQuery = "%5BREDACTED%5D"
		} else {
			redacted := url.Values{}
			for key := range values {
				redacted.Set(key, "[REDACTED]")
			}
			out.RawQuery = redacted.Encode()
		}
	}
	return out.String()
}

var (
	longTokenRe = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{24,}|[a-z0-9_+/=-]{40,})\b`)
	kvSecretRe  = regexp.MustCompile(`(?i)((?:token|authorization|password|secret|uuid|code)\s*[=:]\s*)[^\s<>,;]+`)
	urlRe       = regexp.MustCompile(`https?://[^\s<>"']+`)
)

// scrubSecrets collects values that must never reach the log.
func scrubSecrets(req *http.Request, resp *http.Response, body []byte) []string {
	var secrets []string
	addQuery := func(raw string) {
		for _, pair := range strings.Split(raw, "&") {
			i := strings.IndexByte(pair, '=')
			if i < 0 {
				continue
			}
			v, err := url.QueryUnescape(pair[i+1:])
			if err != nil {
				v = pair[i+1:]
			}
			if len(v) >= 4 {
				secrets = append(secrets, v)
			}
		}
	}
	if req != nil {
		addQuery(req.URL.RawQuery)
		if len(body) > 0 {
			addQuery(string(body))
		}
		if cookie := req.Header.Get("Cookie"); cookie != "" {
			for _, part := range strings.Split(cookie, ";") {
				if i := strings.IndexByte(part, '='); i >= 0 {
					secrets = append(secrets, strings.TrimSpace(part[i+1:]))
				}
			}
		}
	}
	if resp != nil {
		if resp.Request != nil {
			addQuery(resp.Request.URL.RawQuery)
		}
		for _, c := range resp.Cookies() {
			secrets = append(secrets, c.Value)
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	return secrets
}

// scrubText mirrors login_diagnostics.scrub.
func scrubText(text string, req *http.Request, resp *http.Response, body []byte) string {
	seen := map[string]bool{}
	for _, secret := range scrubSecrets(req, resp, body) {
		if secret == "" || seen[secret] {
			continue
		}
		seen[secret] = true
		text = strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	text = urlRe.ReplaceAllStringFunc(text, safeURL)
	text = longTokenRe.ReplaceAllString(text, "[REDACTED]")
	text = kvSecretRe.ReplaceAllString(text, "${1}[REDACTED]")
	return text
}

// LoginLog writes one redacted JSON object per request, matching the shape of
// the Python login_diagnostics.LoginLog records.
type LoginLog struct {
	Path            string
	ProxyConfigured bool
	TrustEnv        bool

	mu  sync.Mutex
	f   *os.File
	err error
}

// NewLoginLog creates a fresh log file with mode 0600.
func NewLoginLog(dir string) (*LoginLog, error) {
	if dir == "" {
		dir = DefaultLogDir
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("无法创建日志目录: %w", err)
	}
	name := fmt.Sprintf("%s-%d.jsonl", time.Now().UTC().Format("20060102T150405"), time.Now().UnixNano())
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("无法创建日志文件: %w", err)
	}
	return &LoginLog{Path: path, f: f}, nil
}

// Close releases the log file.
func (l *LoginLog) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	err := l.f.Close()
	l.f = nil
	return err
}

var loggedRequestHeaders = []string{
	"User-Agent", "Accept", "Accept-Language", "Content-Type", "Origin", "Referer", "X-Requested-With",
}

var loggedResponseHeaders = map[string]bool{
	"content-type": true, "content-length": true, "server": true, "via": true,
	"date": true, "x-cache": true, "retry-after": true, "cf-ray": true, "x-request-id": true,
}

// Record appends one redacted record. Response bodies are only read for error
// statuses, so successful responses are never buffered.
func (l *LoginLog) Record(req *http.Request, resp *http.Response, body []byte, elapsed time.Duration) {
	if l == nil || l.f == nil {
		return
	}
	reqHeaders := map[string]string{}
	if req != nil {
		for _, name := range loggedRequestHeaders {
			if v := req.Header.Get(name); v != "" {
				if name == "Origin" || name == "Referer" {
					v = safeURL(v)
				}
				reqHeaders[name] = v
			}
		}
	}
	respHeaders := map[string]string{}
	for name, values := range resp.Header {
		lower := strings.ToLower(name)
		if !loggedResponseHeaders[lower] {
			continue
		}
		if len(values) > 0 {
			respHeaders[lower] = scrubText(values[0], req, resp, body)
		}
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		respHeaders["Location"] = safeURL(loc)
	}
	sentCookies := []string{}
	if req != nil {
		for _, part := range strings.Split(req.Header.Get("Cookie"), ";") {
			if i := strings.IndexByte(part, '='); i >= 0 {
				sentCookies = append(sentCookies, strings.TrimSpace(part[:i]))
			}
		}
	}
	receivedCookies := []string{}
	for _, c := range resp.Cookies() {
		receivedCookies = append(receivedCookies, c.Name)
	}
	method := ""
	rawURL := ""
	if req != nil {
		method = req.Method
		rawURL = req.URL.String()
	}
	record := map[string]any{
		"time_utc":              time.Now().UTC().Format(time.RFC3339Nano),
		"method":                method,
		"url":                   safeURL(rawURL),
		"status":                resp.StatusCode,
		"elapsed_ms":            elapsed.Milliseconds(),
		"request_headers":       reqHeaders,
		"response_headers":      respHeaders,
		"proxy_configured":      l.ProxyConfigured,
		"trust_env":             l.TrustEnv,
		"sent_cookie_names":     sortedUnique(sentCookies),
		"received_cookie_names": sortedUnique(receivedCookies),
	}
	if resp.StatusCode >= 400 {
		excerpt, size := readErrorBody(resp)
		record["error_body_excerpt"] = scrubText(excerpt, req, resp, body)
		record["body_bytes"] = size
	}
	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	if _, err := l.f.Write(append(line, '\n')); err != nil && l.err == nil {
		l.err = err
	}
	_ = l.f.Sync()
}

func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// readErrorBody reads at most 8192 bytes for the log excerpt and reports the
// full size, restoring the body for the caller.
func readErrorBody(resp *http.Response) (string, int64) {
	if resp.Body == nil {
		return "", 0
	}
	full, err := readAllLimited(resp.Body, 1<<20)
	if err != nil {
		return "", 0
	}
	restoreBody(resp, full)
	if len(full) > 8192 {
		return string(full[:8192]), int64(len(full))
	}
	return string(full), int64(len(full))
}

// Event appends one audit record that is not an HTTP exchange. It exists so a
// simulated write -- a request that was prepared and logged but deliberately not
// sent -- still leaves a durable, redacted trace in the same log the diagnostics
// view reads.
func (l *LoginLog) Event(kind string, fields map[string]any) {
	if l == nil || l.f == nil {
		return
	}
	record := map[string]any{
		"time_utc":         time.Now().UTC().Format(time.RFC3339Nano),
		"event":            kind,
		"proxy_configured": l.ProxyConfigured,
		"trust_env":        l.TrustEnv,
	}
	for name, value := range fields {
		switch v := value.(type) {
		case string:
			record[name] = scrubText(v, nil, nil, nil)
		default:
			record[name] = v
		}
	}
	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	if _, err := l.f.Write(append(line, '\n')); err != nil && l.err == nil {
		l.err = err
	}
	_ = l.f.Sync()
}
