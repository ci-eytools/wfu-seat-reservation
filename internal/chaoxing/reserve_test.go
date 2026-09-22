package chaoxing

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The seat page is the only authority on where its form goes, so these cases pin
// the parser against the shapes a page can actually use.
func TestParseSubmitTargetFromThePageForm(t *testing.T) {
	cases := []struct {
		name   string
		page   string
		want   string
		method string
		ok     bool
	}{
		{
			name: "form that carries submit_enc",
			page: `<html><body>
				<form id="other" action="/data/apps/seat/wrong" method="post"></form>
				<form id="seat" action="/data/apps/seat/room/select" method="post">
					<input type="hidden" id="submit_enc" value="token">
				</form></body></html>`,
			want:   "/data/apps/seat/room/select",
			method: "POST",
			ok:     true,
		},
		{
			name:   "single quotes and an absolute url",
			page:   `<form action='https://office.chaoxing.com/data/apps/seat/room/select' method='get'><input id='submit_enc' value='t'></form>`,
			want:   "https://office.chaoxing.com/data/apps/seat/room/select",
			method: "GET",
			ok:     true,
		},
		{
			name:   "no method means post",
			page:   `<form action="/data/apps/seat/room/select"><input id="submit_enc" value="t"></form>`,
			want:   "/data/apps/seat/room/select",
			method: "POST",
			ok:     true,
		},
		{
			name: "no form at all",
			page: `<html><body><input type="hidden" id="submit_enc" value="t"></body></html>`,
			ok:   false,
		},
		{
			// A script-built action means the page submits in JavaScript, which is
			// exactly when the configured override is needed.
			name: "javascript action is not a target",
			page: `<form action="javascript:void(0)"><input id="submit_enc" value="t"></form>`,
			ok:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, ok := parseSubmitTarget(tc.page)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (target %+v)", ok, tc.ok, target)
			}
			if !ok {
				return
			}
			if target.URL != tc.want {
				t.Errorf("url = %q, want %q", target.URL, tc.want)
			}
			if target.Method != tc.method {
				t.Errorf("method = %q, want %q", target.Method, tc.method)
			}
			if target.Source == "" {
				t.Error("a target must say where it came from")
			}
		})
	}
}

// Waitlist entry points are only ever proposed: the parser collects what the page
// script mentions, and nothing is sent from here.

// A relative action resolves against the office origin, and anything off that host
// is refused rather than armed.
func TestResolveTargetStaysOnTheOfficeHost(t *testing.T) {
	if got, ok := resolveTarget("/data/apps/seat/room/select"); !ok || got != Origin+"/data/apps/seat/room/select" {
		t.Errorf("relative action resolved to %q (%v)", got, ok)
	}
	if _, ok := resolveTarget("https://evil.example/data/apps/seat/room/select"); ok {
		t.Error("a target on another host must not resolve")
	}
}

// The transport refuses everything until a target is armed, and arming is narrow:
// seat API namespace, office host, GET/POST only.
func TestGuardArmsOnlyAnExplicitSeatTarget(t *testing.T) {
	guard := NewGuardedTransport(nil)

	if _, err := guard.Arm("POST", "https://office.chaoxing.com/data/apps/seat/room/select"); err != nil {
		t.Fatalf("arming the page's own target failed: %v", err)
	}
	if _, ok := guard.Armed("POST", "https://office.chaoxing.com/data/apps/seat/room/select"); !ok {
		t.Error("the armed target is not reported as armed")
	}

	// The read-only check is unchanged: it still refuses the same request.
	if err := CheckRequest("POST", "https://office.chaoxing.com/data/apps/seat/room/select"); err == nil {
		t.Error("CheckRequest must still refuse an unarmed write")
	}

	// Arming is a deliberate, narrow act: only the seat API namespace on the
	// office host, and only GET/POST.
	refused := []struct {
		method, url string
	}{
		{"POST", "https://evil.example/data/apps/seat/room/select"},
		{"POST", "https://office.chaoxing.com/other/path"},
		{"DELETE", "https://office.chaoxing.com/data/apps/seat/room/select"},
		{"GET", "https://office.chaoxing.com/front/third/apps/seat/select"},
	}
	for _, tc := range refused {
		if _, err := guard.Arm(tc.method, tc.url); err == nil {
			t.Errorf("Arm(%s %s) should have been refused", tc.method, tc.url)
		}
		if _, ok := guard.Armed(tc.method, tc.url); ok {
			t.Errorf("%s %s must not be armed", tc.method, tc.url)
		}
	}

	// Another seat API path is not armed just because a sibling was: the user
	// named one target, and that is the only one that opens.
	if _, ok := guard.Armed("POST", "https://office.chaoxing.com/data/apps/seat/room/other"); ok {
		t.Error("arming one target must not arm its neighbours")
	}
	if _, ok := guard.Armed("POST", "https://evil.example/data/apps/seat/room/select"); ok {
		t.Error("the same path on another host must not count as armed")
	}

	guard.Disarm()
	if _, ok := guard.Armed("POST", "https://office.chaoxing.com/data/apps/seat/room/select"); ok {
		t.Error("Disarm left a target armed")
	}
}

// The armed transport lets exactly one request through, and the policy still
// counts everything else it refused.
func TestArmedTransportSendsOnlyTheNamedTarget(t *testing.T) {
	fake := &fakeTransport{handler: func(req *http.Request) *http.Response {
		return jsonResponse(req, 200, nil, `{"status":true,"msg":"ok"}`)
	}}
	guard := NewGuardedTransport(fake)

	if _, err := guard.Arm("POST", "https://office.chaoxing.com/data/apps/seat/room/select"); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	req, _ := http.NewRequest("POST", "https://office.chaoxing.com/data/apps/seat/room/select", nil)
	if _, err := guard.RoundTrip(req); err != nil {
		t.Fatalf("the armed target was refused: %v", err)
	}

	// A nearby path is still blocked, and counted.
	other, _ := http.NewRequest("POST", "https://office.chaoxing.com/data/apps/seat/room/cancel", nil)
	if _, err := guard.RoundTrip(other); err == nil {
		t.Fatal("an unarmed write reached the transport")
	}
	if got := guard.BlockedCount(); got != 1 {
		t.Errorf("blocked count = %d, want 1", got)
	}
}

// Without a target, submission refuses instead of inventing an endpoint.
func TestSubmitRefusesWithoutATarget(t *testing.T) {
	client, _ := newSelectionFixture(t)
	ctx := context.Background()
	if _, err := client.Choose(ctx, "001", "09:00", "09:30"); err != nil {
		t.Fatalf("Choose: %v", err)
	}
	// The fixture never opened a room, so no page target was recorded.
	_, err := client.SubmitReservation(ctx)
	if !IsUnsupported(err) {
		t.Fatalf("SubmitReservation error = %v, want unsupported", err)
	}
	if !strings.Contains(Message(err), "提交地址") {
		t.Errorf("the refusal should say what is missing: %v", Message(err))
	}
	// And the configured override is honoured instead.
	client.SetReserveConfig(ReserveConfig{SubmitURL: "/data/apps/seat/room/select", Method: "POST"})
	targets := client.ReserveTargets()
	if !targets.HasSubmit || targets.Submit.Path() != "/data/apps/seat/room/select" {
		t.Fatalf("configured target not used: %+v", targets)
	}
}

// The waitlist request signs the same parameter set with no seat and no times.

// A submitted reservation reports the service's verdict and records that it was
// answered, and exactly one request is sent.
func TestSubmitReservationSendsOneRequestAndReportsTheVerdict(t *testing.T) {
	client, fake := newSelectionFixture(t)
	ctx := context.Background()
	if _, err := client.Choose(ctx, "001", "09:00", "09:30"); err != nil {
		t.Fatalf("Choose: %v", err)
	}

	writes := 0
	fake.handler = func(req *http.Request) *http.Response {
		if req.Method == http.MethodPost && req.URL.Path == "/data/apps/seat/room/select" {
			writes++
			return jsonResponse(req, 200, nil, `{"status":true,"msg":"预约成功"}`)
		}
		return jsonResponse(req, 200, nil, `{"status":true,"data":{"seatReserves":[{"seatNum":"002"}]}}`)
	}
	client.SetReserveConfig(ReserveConfig{SubmitURL: "/data/apps/seat/room/select"})

	result, err := client.SubmitReservation(ctx)
	if err != nil {
		t.Fatalf("SubmitReservation: %v", err)
	}
	if !result.OK || result.Message != "预约成功" {
		t.Fatalf("result = %+v", result)
	}
	if writes != 1 {
		t.Fatalf("the reservation was sent %d times, want exactly 1", writes)
	}
	if !client.Current.Submitted || client.Current.Summary().State != "submitted" {
		t.Errorf("the selection was not marked as answered: %+v", client.Current.Summary())
	}
}

// A response the client cannot read is never reported as success.
func TestUnreadableWriteResponseIsNotSuccess(t *testing.T) {
	cases := []struct {
		body   string
		status int
		ok     bool
	}{
		{`{"status":false,"msg":"该座位已被占用"}`, 200, false},
		{`{"success":true,"msg":"ok"}`, 200, true},
		{`<html>系统繁忙</html>`, 500, false},
		// A readable message that is not a status is reported with its text, and
		// is not treated as a refusal on its own.
		{`{"msg":"请先登录"}`, 200, false},
		// Text we cannot read is never a confirmation, even with a 200.
		{`<html><body>请登录</body></html>`, 200, false},
		{``, 204, false},
	}
	for _, tc := range cases {
		resp := &Response{StatusCode: tc.status, Body: []byte(tc.body)}
		ok, message := interpretWriteResponse(resp)
		if ok != tc.ok {
			t.Errorf("body %q status %d: ok = %v, want %v (%s)", tc.body, tc.status, ok, tc.ok, message)
		}
		if strings.TrimSpace(message) == "" {
			t.Errorf("body %q produced no message", tc.body)
		}
	}
}
