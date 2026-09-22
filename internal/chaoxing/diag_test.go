package chaoxing

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

// The diagnostics log must never contain a token, cookie value, or query value.
func TestLogRedactsSecrets(t *testing.T) {
	const (
		querySecret  = "supersecretqueryvalue"
		cookieSecret = "topsecretcookievalue"
		bodySecret   = "bodytokenvalue1234567890"
	)
	fake := &fakeTransport{handler: func(req *http.Request) *http.Response {
		return jsonResponse(req, 200, http.Header{
			"Set-Cookie": {"sessionid=" + cookieSecret + "; Path=/"},
		}, `{"success":false,"msg":"denied"}`)
	}}
	dir := t.TempDir()
	session, err := NewSessionWithConfig(SessionConfig{BaseTransport: fake, LogDir: dir})
	if err != nil {
		t.Fatalf("NewSessionWithConfig: %v", err)
	}
	defer session.Close()
	if session.Log == nil {
		t.Fatal("log was not created")
	}

	_, err = session.PostForm(context.Background(),
		"https://office.chaoxing.com/data/apps/seat/getusedseatnums?token="+querySecret,
		url.Values{"password": {bodySecret}})
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	raw, err := os.ReadFile(session.Log.Path)
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	content := string(raw)
	if content == "" {
		t.Fatal("log is empty")
	}
	for _, secret := range []string{querySecret, cookieSecret, bodySecret} {
		if strings.Contains(content, secret) {
			t.Errorf("log leaked %q:\n%s", secret, content)
		}
	}
	// The URL is rendered through SafeURL, which percent-encodes the marker.
	if !strings.Contains(content, "%5BREDACTED%5D") {
		t.Errorf("log did not redact the query value:\n%s", content)
	}
	for _, want := range []string{`"status":200`, `"method":"POST"`} {
		if !strings.Contains(content, want) {
			t.Errorf("log missing %s:\n%s", want, content)
		}
	}

	info, err := os.Stat(session.Log.Path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("log permissions = %o, want 600", perm)
	}
}

// Error responses do have their body logged, so the body must be scrubbed.
func TestLogScrubsErrorBody(t *testing.T) {
	const bodySecret = "bodytokenvalue1234567890"
	fake := &fakeTransport{handler: func(req *http.Request) *http.Response {
		return jsonResponse(req, 403, nil, `{"msg":"denied","echo":"token=`+bodySecret+`"}`)
	}}
	dir := t.TempDir()
	session, err := NewSessionWithConfig(SessionConfig{BaseTransport: fake, LogDir: dir})
	if err != nil {
		t.Fatalf("NewSessionWithConfig: %v", err)
	}
	defer session.Close()

	if _, err := session.PostForm(context.Background(),
		"https://office.chaoxing.com/data/apps/seat/getusedseatnums",
		url.Values{"token": {bodySecret}}); err != nil {
		t.Fatalf("request: %v", err)
	}
	raw, err := os.ReadFile(session.Log.Path)
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	content := string(raw)
	if !strings.Contains(content, "error_body_excerpt") {
		t.Fatalf("error body was not logged:\n%s", content)
	}
	if strings.Contains(content, bodySecret) {
		t.Errorf("error excerpt leaked the secret:\n%s", content)
	}
	if !strings.Contains(content, "[REDACTED]") {
		t.Errorf("error excerpt was not redacted:\n%s", content)
	}
}

func TestSafeURLRedactsQueryValues(t *testing.T) {
	got := safeURL("https://office.chaoxing.com/x?token=abc&fidEnc=35bbd135397006a8")
	if strings.Contains(got, "abc") || strings.Contains(got, "35bbd135397006a8") {
		t.Fatalf("SafeURL leaked a query value: %s", got)
	}
	if !strings.Contains(got, "%5BREDACTED%5D") {
		t.Fatalf("SafeURL did not redact: %s", got)
	}
}

func TestScrubRedactsInlineSecrets(t *testing.T) {
	body := []byte("password=longenoughvalue")
	text := "Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789ABCDEF and code=99887766"
	scrubbed := scrubText(text, nil, nil, body)
	if strings.Contains(scrubbed, "abcdefghijklmnopqrstuvwxyz0123456789ABCDEF") {
		t.Errorf("long token survived: %s", scrubbed)
	}
	if strings.Contains(scrubbed, "99887766") {
		t.Errorf("code survived: %s", scrubbed)
	}
	if !strings.Contains(scrubbed, "[REDACTED]") {
		t.Errorf("nothing was redacted: %s", scrubbed)
	}
}
