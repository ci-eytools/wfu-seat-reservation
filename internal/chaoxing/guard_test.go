package chaoxing

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Ported from tests/test_selection.py::GuardTests.
func TestMutationsBlockedBeforeNetwork(t *testing.T) {
	cases := []struct{ method, path string }{
		{http.MethodPost, "submit"},
		{http.MethodGet, "submit"},
		{http.MethodGet, "cancel"},
		{http.MethodGet, "sign"},
		{http.MethodPost, "supervise"},
		{http.MethodGet, "%73ubmit"}, // percent-encoded 's'
	}
	for _, tc := range cases {
		raw := "https://office.chaoxing.com/data/apps/seat/" + tc.path
		if err := CheckRequest(tc.method, raw); !IsBlocked(err) {
			t.Errorf("%s %s: want BlockedSeatRequest, got %v", tc.method, raw, err)
		}
	}
}

func TestUnknownOfficeEndpointDenied(t *testing.T) {
	if err := CheckRequest(http.MethodGet, "https://office.chaoxing.com/unknown/action"); !IsBlocked(err) {
		t.Fatalf("want blocked, got %v", err)
	}
}

func TestReadQueriesAllowed(t *testing.T) {
	allowed := []struct{ method, path string }{
		{http.MethodGet, "/data/apps/seat/config"},
		{http.MethodGet, "/data/apps/seat/index"},
		{http.MethodGet, "/data/apps/seat/room/list"},
		{http.MethodGet, "/data/apps/seat/room/reserve-window/check"},
		{http.MethodPost, "/data/apps/seat/room/info"},
		{http.MethodPost, "/data/apps/seat/room/info/switch"},
		{http.MethodPost, "/data/apps/seat/getusedtimes"},
		{http.MethodPost, "/data/apps/seat/getusedseatnums"},
		{http.MethodGet, "/data/apps/seat/getdrawseat"},
		{http.MethodGet, "/data/apps/seat/seatgrid/roomid"},
		{http.MethodGet, "/front/third/apps/seat/index"},
		{http.MethodGet, "/front/third/apps/seat/list"},
		{http.MethodGet, "/front/third/apps/seat/select"},
	}
	for _, tc := range allowed {
		if err := CheckRequest(tc.method, "https://office.chaoxing.com"+tc.path); err != nil {
			t.Errorf("%s %s: want allowed, got %v", tc.method, tc.path, err)
		}
	}
}

// A whitelisted path with the wrong method must still be refused.
func TestWrongMethodOnWhitelistedPathDenied(t *testing.T) {
	cases := []struct{ method, path string }{
		{http.MethodPost, "/data/apps/seat/room/list"},
		{http.MethodPost, "/data/apps/seat/seatgrid/roomid"},
		{http.MethodGet, "/data/apps/seat/room/info"},
		{http.MethodPost, "/front/third/apps/seat/index"},
		{http.MethodPost, "/front/third/apps/seat/select"},
	}
	for _, tc := range cases {
		if err := CheckRequest(tc.method, "https://office.chaoxing.com"+tc.path); !IsBlocked(err) {
			t.Errorf("%s %s: want blocked, got %v", tc.method, tc.path, err)
		}
	}
}

// The seat APIs must only ever be reached on the office host.
func TestSeatAPIsOnForeignHostDenied(t *testing.T) {
	for _, host := range []string{"evil.example.com", "office.chaoxing.com.evil.example.com"} {
		raw := "https://" + host + "/data/apps/seat/getusedseatnums"
		if err := CheckRequest(http.MethodPost, raw); !IsBlocked(err) {
			t.Errorf("%s: want blocked, got %v", raw, err)
		}
	}
}

func TestBlockedRequestNeverReachesTransport(t *testing.T) {
	fake := &fakeTransport{}
	login := testLogin(t, fake)

	if _, err := login.Session.PostForm(context.Background(),
		"https://office.chaoxing.com/data/apps/seat/submit", url.Values{}); !IsBlocked(err) {
		t.Fatalf("want blocked, got %v", err)
	}
	if fake.callCount() != 0 {
		t.Fatalf("blocked request reached the transport: %v", fake.calledPaths())
	}
	if got := login.BlockedSeatRequests(); got != 1 {
		t.Fatalf("blocked counter = %d, want 1", got)
	}
}

func TestAllowedQueryReachesTransport(t *testing.T) {
	fake := &fakeTransport{handler: func(req *http.Request) *http.Response {
		return jsonResponse(req, 200, nil, `{"success":true,"data":{}}`)
	}}
	login := testLogin(t, fake)

	if _, err := login.Session.PostForm(context.Background(),
		"https://office.chaoxing.com/data/apps/seat/getusedseatnums",
		url.Values{"roomId": {"6299"}}); err != nil {
		t.Fatalf("allowed query failed: %v", err)
	}
	if fake.callCount() != 1 {
		t.Fatalf("call count = %d, want 1", fake.callCount())
	}
	if got := login.BlockedSeatRequests(); got != 0 {
		t.Fatalf("blocked counter = %d, want 0", got)
	}
}

// Every redirect hop goes through the guard, so a redirect into a mutating seat
// endpoint is refused even by a client that follows redirects automatically.
func TestRedirectHopIsChecked(t *testing.T) {
	fake := &fakeTransport{handler: func(req *http.Request) *http.Response {
		if strings.HasSuffix(req.URL.Path, "/front/third/apps/seat/index") {
			return jsonResponse(req, 302, http.Header{
				"Location": {"https://office.chaoxing.com/data/apps/seat/submit"},
			}, "")
		}
		return jsonResponse(req, 200, nil, "{}")
	}}
	following := &http.Client{Transport: NewGuardedTransport(fake)}

	_, err := following.Get("https://office.chaoxing.com/front/third/apps/seat/index")
	if !IsBlocked(err) {
		t.Fatalf("redirected request: want BlockedSeatRequest, got %v", err)
	}
	if fake.callCount() != 1 {
		t.Fatalf("only the first hop may reach the network, got %v", fake.calledPaths())
	}
}
