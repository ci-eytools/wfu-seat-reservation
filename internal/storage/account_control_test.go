package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAccountSwitchBlocksClaimsAndDeleteIsIsolated(t *testing.T) {
	root := t.TempDir()
	a, _ := AccountDir(root, "张20260001")
	b, _ := AccountDir(root, "李20260002")
	db, e := Open(a)
	if e != nil {
		t.Fatal(e)
	}
	j := Job{ID: "job", State: "pending", Next: time.Now()}
	db.Add(j)
	db.SetEnabled(false)
	if ok, e := db.Claim(j, time.Now()); e != nil || ok {
		t.Fatal("disabled claim", ok, e)
	}
	db.SetEnabled(true)
	if ok, e := db.Claim(j, time.Now()); e != nil || !ok {
		t.Fatal(ok, e)
	}
	if e := RemoveAccount(root, "张20260001"); e == nil {
		t.Fatal("removed executing account")
	}
	j.State = "succeeded"
	db.Finish(j, j.Next)
	db.Close()
	if e := RemoveAccount(root, "张20260001"); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(a); !os.IsNotExist(e) {
		t.Fatal("credentials remain")
	}
	if _, e := os.Stat(b); e != nil {
		t.Fatal("other account removed", e)
	}
}
func TestRenamePreservesCredentialsWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	dir, _ := AccountDir(root, "default")
	db, _ := Open(dir)
	db.Put("cookies", []byte("fixture"))
	db.Close()
	if e := RenameAccount(root, "default", "张20260001"); e != nil {
		t.Fatal(e)
	}
	db, _ = Open(filepath.Join(root, "accounts", "张20260001"))
	defer db.Close()
	b, _ := db.Get("cookies")
	if string(b) != "fixture" {
		t.Fatal("lost credentials")
	}
	AccountDir(root, "pending-1")
	if e := RenameAccount(root, "pending-1", "张20260001"); e == nil {
		t.Fatal("overwrote identity")
	}
}
