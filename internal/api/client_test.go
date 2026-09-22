package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestEndpointValidationAndSeparation(t *testing.T) {
	for _, raw := range []string{"http://public.example", "https://name:pass@example.com", "https://example.com/?token=secret", "https://example.com/#secret", "file:///tmp/test"} {
		if _, e := NewClient(raw, t.TempDir()); e == nil {
			t.Fatal("accepted", raw)
		}
	}
	root := t.TempDir()
	a, e := NewClient("https://EXAMPLE.com/", root)
	if e != nil {
		t.Fatal(e)
	}
	b, e := NewClient("https://example.com", root)
	if e != nil {
		t.Fatal(e)
	}
	other, e := NewClient("https://another.example", root)
	if e != nil {
		t.Fatal(e)
	}
	if a.ProfileRoot != b.ProfileRoot || a.ProfileRoot == other.ProfileRoot {
		t.Fatal("server namespace mismatch")
	}
}
func TestClientNeverForwardsBearerOnRedirect(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	c, e := NewClient(redirect.URL, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = c.call(context.Background(), "GET", "/v1/me", "secret", "", nil, nil); e == nil {
		t.Fatal("redirect accepted")
	}
	if reached.Load() {
		t.Fatal("bearer redirected")
	}
}
func TestCancelledStartCannotInstallLateCapability(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var cancelled atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			cancelled.Store(true)
			w.Write([]byte("{}"))
			return
		}
		close(entered)
		<-release
		w.Write([]byte(`{"id":"late","secret":"capability","png":"AQ==","state":"waiting"}`))
	}))
	defer server.Close()
	c, e := NewClient(server.URL, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := c.Start(context.Background()); done <- e }()
	<-entered
	c.CancelLogin()
	close(release)
	if e := <-done; e == nil {
		t.Fatal("cancelled QR installed")
	}
	if !cancelled.Load() {
		t.Fatal("server QR not cancelled")
	}
	if c.loginID != "" {
		t.Fatal("late capability retained")
	}
}
