package chaoxing

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// fakeTransport is a RoundTripper that records requests and returns canned
// responses. It never touches the network, which lets the policy and the client
// logic be tested against the real host names.
type fakeTransport struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   []string
	handler  func(req *http.Request) *http.Response
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body string
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err == nil {
			body = string(b)
			req.Body = io.NopCloser(bytes.NewReader(b))
		}
	}
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.bodies = append(f.bodies, body)
	handler := f.handler
	f.mu.Unlock()

	if handler == nil {
		return jsonResponse(req, 200, nil, "{}"), nil
	}
	resp := handler(req)
	if resp.Request == nil {
		resp.Request = req
	}
	return resp, nil
}

func (f *fakeTransport) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeTransport) calledPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.requests))
	for _, r := range f.requests {
		out = append(out, r.URL.Path)
	}
	return out
}

func (f *fakeTransport) lastBody() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		return ""
	}
	return f.bodies[len(f.bodies)-1]
}

func jsonResponse(req *http.Request, status int, header http.Header, body string) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	if header.Get("Content-Type") == "" && body != "" {
		header.Set("Content-Type", "application/json;charset=utf-8")
	}
	return &http.Response{
		StatusCode:    status,
		Header:        header,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

func intPtr(v int) *int { return &v }

func testLogin(t *testing.T, fake *fakeTransport) *QRLogin {
	t.Helper()
	session, err := NewSessionWithConfig(SessionConfig{BaseTransport: fake})
	if err != nil {
		t.Fatalf("NewSessionWithConfig: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &QRLogin{Session: session, Tokens: map[string]string{}}
}
