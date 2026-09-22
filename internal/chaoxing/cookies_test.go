package chaoxing

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestCookiePersistencePreservesScopeAndDeletion(t *testing.T) {
	u, _ := url.Parse("https://office.chaoxing.com/private/login")
	var saved []byte
	j := newPersistentJar()
	j.save = func(b []byte) error { saved = append([]byte(nil), b...); return nil }
	j.SetCookies(u, []*http.Cookie{{Name: "host", Value: "one", Secure: true}, {Name: "domain", Value: "two", Domain: ".chaoxing.com", Path: "/", Secure: true}, {Name: "expired", Value: "no", Expires: time.Now().Add(-time.Hour)}})
	restored := newPersistentJar()
	if err := restored.restore(saved); err != nil {
		t.Fatal(err)
	}
	if got := restored.Cookies(u); len(got) != 2 {
		t.Fatalf("%v", got)
	}
	sibling, _ := url.Parse("https://passport2.chaoxing.com/private/login")
	got := restored.Cookies(sibling)
	if len(got) != 1 || got[0].Name != "domain" {
		t.Fatalf("host-only leaked: %v", got)
	}
	outside, _ := url.Parse("https://example.com/private/login")
	if len(restored.Cookies(outside)) != 0 {
		t.Fatal("foreign domain leaked")
	}
	root, _ := url.Parse("https://office.chaoxing.com/")
	if got := restored.Cookies(root); len(got) != 1 || got[0].Name != "domain" {
		t.Fatalf("path not preserved %v", got)
	}
	j.SetCookies(u, []*http.Cookie{{Name: "host", MaxAge: -1}})
	again := newPersistentJar()
	again.restore(saved)
	if len(again.Cookies(u)) != 1 {
		t.Fatal("deleted cookie restored")
	}
}
func TestAmbiguousResponsesNeverFallback(t *testing.T) {
	for _, body := range []string{"", `{"msg":"请登录"}`, `{"status":"maybe"}`, `<html>login</html>`} {
		r := &Response{StatusCode: 200, Body: []byte(body)}
		if explicitRejection(r) {
			t.Fatal(body)
		}
		if ok, _ := interpretWriteResponse(r); ok {
			t.Fatal(body)
		}
	}
}
