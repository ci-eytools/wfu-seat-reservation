package config

import (
	"strings"
	"testing"
)

func TestAccountUserAgentPersistence(t *testing.T) {
	root := t.TempDir()
	a, _, e := LoadAccount(root, "张20260001")
	if e != nil {
		t.Fatal(e)
	}
	b, _, e := LoadAccount(root, "李20260002")
	if e != nil {
		t.Fatal(e)
	}
	if a.UserAgent == b.UserAgent || a.UserAgent == "" {
		t.Fatal("shared UA")
	}
	if strings.Contains(a.UserAgent, "20260001") {
		t.Fatal("student ID in UA")
	}
	if _, e = Save(a); e != nil {
		t.Fatal(e)
	}
	again, _, e := LoadAccount(root, a.Account)
	if e != nil || again.UserAgent != a.UserAgent {
		t.Fatal("UA not persistent", e)
	}
}
