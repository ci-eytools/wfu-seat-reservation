package chaoxing

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// callbackFixture reproduces the structure of the real WFU callback page
// (work/callback_page_redacted.html) with non-secret values substituted. It
// exists to pin the RE2 rewrite of the original lookaround-based regexes.
const callbackFixture = `<!DOCTYPE html>
<html><head><script type="text/javascript">
    var url = "\/OAuth2\/wfu\/login";
    jQuery.ajax({
        url: url,
        async: false,
        data: {
            data: "Zm9vYmFyLXRva2Vu",
            time: 1789555039354,
            enc: "AAAA+BBB/CCC=",
            displayName: "\u6602\u667A\u4F1F",
            userRole: 3
        },
        dataType: "json",
        success: function (data) {
            if (data.status) { res = 1; }
        }
    });
</script></head></html>`

// callbackParamMap adapts the ordered parser output to a map for assertions.
func callbackParamMap(body string) (map[string]string, error) {
	pairs, err := callbackParams(body)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, p := range pairs {
		out[p.Key] = p.Value
	}
	return out, nil
}

func TestCallbackParamsAreParsedFaithfully(t *testing.T) {
	params, err := callbackParamMap(callbackFixture)
	if err != nil {
		t.Fatalf("callbackParams: %v", err)
	}
	want := map[string]string{
		"data":        "Zm9vYmFyLXRva2Vu",
		"time":        "1789555039354",
		"enc":         "AAAA+BBB/CCC=",
		"displayName": "昂智伟",
		"userRole":    "3",
	}
	if !reflect.DeepEqual(params, want) {
		t.Fatalf("params = %#v, want %#v", params, want)
	}
	// The `data` field must not be taken from `dataType: "json"`.
	if params["data"] == "json" {
		t.Fatal("data was misparsed from dataType")
	}
}

func TestCallbackParamOrderIsPreserved(t *testing.T) {
	pairs, err := callbackParams(callbackFixture)
	if err != nil {
		t.Fatalf("callbackParams: %v", err)
	}
	got := encodePairs(pairs)
	want := "data=Zm9vYmFyLXRva2Vu" +
		"&time=1789555039354" +
		"&enc=" + url.QueryEscape("AAAA+BBB/CCC=") +
		"&displayName=" + url.QueryEscape("昂智伟") +
		"&userRole=3"
	if got != want {
		t.Fatalf("encoded params =\n%s\nwant\n%s", got, want)
	}
}

func TestCallbackParamsRejectRedactedTemplate(t *testing.T) {
	// The original refuses to work from a scrubbed file so an authorization
	// code is never replayed from a redacted artifact.
	redacted := strings.Replace(callbackFixture, `"Zm9vYmFyLXRva2Vu"`, `"[REDACTED]"`, 1)
	if _, err := callbackParamMap(redacted); err == nil {
		t.Fatal("a redacted callback page must be rejected")
	}
}

func TestCallbackParamsRejectForeignTemplate(t *testing.T) {
	foreign := strings.Replace(callbackFixture, `\/OAuth2\/wfu\/login`, `\/other\/login`, 1)
	if _, err := callbackParamMap(foreign); err == nil {
		t.Fatal("a non-WFU callback template must be rejected")
	}
}

func TestCallbackParamsRejectMissingField(t *testing.T) {
	for _, field := range callbackFields {
		line := ""
		for _, l := range strings.Split(callbackFixture, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), field+":") {
				line = l
				break
			}
		}
		if line == "" {
			t.Fatalf("fixture does not contain field %q", field)
		}
		without := strings.Replace(callbackFixture, line+"\n", "", 1)
		if _, err := callbackParamMap(without); err == nil {
			t.Errorf("a callback missing %q must be rejected", field)
		}
	}
}

func TestStatusAcceptedMatchesPython(t *testing.T) {
	cases := []struct {
		in   any
		want bool
	}{
		{true, true},
		{false, false},
		{float64(1), true},
		{float64(0), false},
		{"1", false}, // Python: "1" == 1 is False
		{nil, false},
	}
	for _, tc := range cases {
		if got := statusAccepted(tc.in); got != tc.want {
			t.Errorf("statusAccepted(%#v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestReadJSStringRequiresJSONString(t *testing.T) {
	body := `var UUID = "abc-123"; var baseImg = "data:image/png;base64,AAAA";`
	uuid, err := readJSString(body, "UUID")
	if err != nil || uuid != "abc-123" {
		t.Fatalf("UUID = %q, err = %v", uuid, err)
	}
	dataURI, err := readJSString(body, "baseImg")
	if err != nil || !strings.HasPrefix(dataURI, "data:image/png;base64,") {
		t.Fatalf("baseImg = %q, err = %v", dataURI, err)
	}
	if _, err := readJSString(body, "missing"); err == nil {
		t.Fatal("a missing variable must be an error")
	}
}

func TestQRPollStates(t *testing.T) {
	cases := []struct {
		name string
		body string
		want QRState
	}{
		{"confirmed", `{"code":"0x000000","data":{"locatUrl":"https://tyrzfw.chaoxing.com/OAuth2/wfu/index"}}`, QRConfirmed},
		{"expired", `{"code":"0x0030010016"}`, QRExpired},
		{"waiting", `{"code":"0x0000000000"}`, QRWaiting},
	}
	for _, tc := range cases {
		fake := &fakeTransport{handler: func(req *http.Request) *http.Response {
			return jsonResponse(req, 200, nil, tc.body)
		}}
		login := testLogin(t, fake)
		login.UUID = "abc"
		state, _, err := login.PollOnce(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if state != tc.want {
			t.Errorf("%s: state = %q, want %q", tc.name, state, tc.want)
		}
	}
}

func TestQRStartRejectsNonPNGDataURI(t *testing.T) {
	fake := &fakeTransport{handler: func(req *http.Request) *http.Response {
		return jsonResponse(req, 200, nil, `var UUID = "abc"; var baseImg = "data:image/gif;base64,AAAA";`)
	}}
	login := testLogin(t, fake)
	if _, err := login.Start(t.Context()); err == nil {
		t.Fatal("a non-PNG data URI must be rejected")
	}
}

// The callback walker must refuse to leave the login flow.
func TestFollowCallbackRefusesSeatAPI(t *testing.T) {
	fake := &fakeTransport{}
	login := testLogin(t, fake)
	_, err := login.FollowCallback(t.Context(), "https://office.chaoxing.com/data/apps/seat/submit")
	if err == nil {
		t.Fatal("a seat API URL must be refused during login")
	}
	if fake.callCount() != 0 {
		t.Fatalf("no request may be issued, got %v", fake.calledPaths())
	}
}

func TestFollowCallbackStopsOnForeignHost(t *testing.T) {
	fake := &fakeTransport{}
	login := testLogin(t, fake)
	result, err := login.FollowCallback(t.Context(), "https://evil.example.com/steal")
	if err != nil {
		t.Fatalf("FollowCallback: %v", err)
	}
	if StateString(result) != "inspect_redirect" {
		t.Fatalf("state = %v", result)
	}
	if fake.callCount() != 0 {
		t.Fatalf("a foreign host must not be requested, got %v", fake.calledPaths())
	}
}
