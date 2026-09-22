package chaoxing

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const ajaxSubmitFixture = `function doSubmit(x){operateData('data/apps/seat/submit', {enc:submitVerify.verifyParam(x)});} function operateData(url, data) { return $.post(url, data) }`

func TestSelectScriptTargetIsReadWithoutSubmitting(t *testing.T) {
	calls := 0
	fake := &fakeTransport{handler: func(r *http.Request) *http.Response {
		calls++
		if r.Method != "GET" || r.URL.Host != "office-static.chaoxing.com" {
			t.Fatal("unexpected request", r.Method, r.URL)
		}
		return jsonResponse(r, 200, nil, ajaxSubmitFixture)
	}}
	c := NewSeatClient(testLogin(t, fake), DefaultFIDEnc, DefaultMappID)
	page := `<script src="https://office-static.chaoxing.com/r/staticreserve/js/src/front/apps/seat/submit/third/seat_select_third.js?t=1"></script>`
	targets := c.loadScriptTarget(context.Background(), page, pageTargets{})
	if !targets.hasSub || targets.submit.Method != "POST" || targets.submit.Path() != "/data/apps/seat/submit" || calls != 1 {
		t.Fatal(targets, calls)
	}
	malicious := strings.ReplaceAll(page, "office-static.chaoxing.com", "other.example")
	c.loadScriptTarget(context.Background(), malicious, pageTargets{})
	if calls != 1 {
		t.Fatal("fetched unrelated script")
	}
}
func TestScriptTargetRequiresObservedPostHelper(t *testing.T) {
	for _, script := range []string{strings.ReplaceAll(ajaxSubmitFixture, "$.post", "$.get"), strings.ReplaceAll(ajaxSubmitFixture, "seat/submit", "seat/delete"), `operateData('data/apps/seat/submit', {})`} {
		if _, ok := parseScriptSubmitTarget(script); ok {
			t.Fatal("guessed target")
		}
	}
	if _, ok := parseScriptSubmitTarget(ajaxSubmitFixture); !ok {
		t.Fatal("POST target not found")
	}
}
