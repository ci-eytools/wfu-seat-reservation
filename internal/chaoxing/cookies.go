package chaoxing

import (
	"encoding/json"
	"golang.org/x/net/publicsuffix"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// PersistentJar preserves domain, host-only scope, path and expiration. A flat
// list of Cookie headers would lose those boundaries between login hosts.
type storedCookie struct {
	Origin string
	Cookie http.Cookie
}
type PersistentJar struct {
	mu      sync.Mutex
	inner   *cookiejar.Jar
	entries map[string]storedCookie
	save    func([]byte) error
	lastErr error
}

func newPersistentJar() *PersistentJar {
	j, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	return &PersistentJar{inner: j, entries: map[string]storedCookie{}}
}
func (j *PersistentJar) Cookies(u *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.inner.Cookies(u)
}
func (j *PersistentJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.inner.SetCookies(u, cookies)
	for _, c := range cookies {
		v := *c
		domain := strings.TrimPrefix(strings.ToLower(v.Domain), ".")
		host := strings.ToLower(u.Hostname())
		if domain != "" && host != domain && !strings.HasSuffix(host, "."+domain) {
			continue
		}
		if domain != "" {
			suffix, _ := publicsuffix.PublicSuffix(domain)
			if suffix == domain && host != domain {
				continue
			}
		}
		if domain == "" {
			domain = host
		}
		if v.Path == "" || v.Path[0] != '/' {
			v.Path = u.Path
			if pos := strings.LastIndex(v.Path, "/"); pos > 0 {
				v.Path = v.Path[:pos]
			} else {
				v.Path = "/"
			}
		}
		key := domain + "\n" + v.Path + "\n" + v.Name
		if v.MaxAge < 0 || (!v.Expires.IsZero() && v.Expires.Before(time.Now())) {
			delete(j.entries, key)
			continue
		}
		if v.MaxAge > 0 {
			v.Expires = time.Now().Add(time.Duration(v.MaxAge) * time.Second)
			v.MaxAge = 0
		}
		origin := u.Scheme + "://" + u.Host + "/"
		j.entries[key] = storedCookie{origin, v}
	}
	j.persist()
}
func (j *PersistentJar) persist() {
	if j.save != nil {
		b, err := json.Marshal(j.entries)
		if err == nil {
			err = j.save(b)
		}
		j.lastErr = err
	}
}
func (j *PersistentJar) restore(b []byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	var records map[string]storedCookie
	if len(b) > 0 {
		if err := json.Unmarshal(b, &records); err != nil {
			return err
		}
	}
	for k, r := range records {
		if !r.Cookie.Expires.IsZero() && !r.Cookie.Expires.After(time.Now()) {
			continue
		}
		u, err := url.Parse(r.Origin)
		if err != nil || u.Scheme != "https" {
			continue
		}
		j.inner.SetCookies(u, []*http.Cookie{&r.Cookie})
		j.entries[k] = r
	}
	return nil
}

// AttachCookies restores this account only. The callback persists future rotations.
func (s *Session) AttachCookies(b []byte, save func([]byte) error) error {
	j := newPersistentJar()
	if err := j.restore(b); err != nil {
		return err
	}
	j.save = save
	s.jar = j
	s.Client.Jar = j
	return nil
}
func (s *Session) CookieSaveError() error {
	if j, ok := s.jar.(*PersistentJar); ok {
		j.mu.Lock()
		defer j.mu.Unlock()
		return j.lastErr
	}
	return nil
}
