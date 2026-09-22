package storage

import (
	"os"
	"sync"
	"testing"
	"time"
)

func TestAccountIsolation(t *testing.T) {
	root := t.TempDir()
	a, _ := AccountDir(root, "alice")
	b, _ := AccountDir(root, "bob")
	first, e := Open(a)
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close()
	second, e := Open(b)
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	first.Put("cookies", []byte("alice private"))
	first.Add(Job{ID: "same", Seats: []string{"001"}, State: "pending", Next: time.Now()})
	raw, _ := second.Get("cookies")
	jobs, _ := second.Jobs()
	if len(raw) != 0 || len(jobs) != 0 {
		t.Fatal("tenant leaked")
	}
	if err := second.Cancel("same"); err == nil {
		t.Fatal("cancelled another tenant")
	}
	if _, err := AccountDir(root, "../alice"); err == nil {
		t.Fatal("traversal allowed")
	}
	mode, _ := os.Stat(a + "/state.db")
	if mode.Mode().Perm() != 0600 {
		t.Fatal(mode.Mode())
	}
}
func TestCancelCannotUndoRunning(t *testing.T) {
	db, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	j := Job{ID: "running", Next: time.Now(), State: "pending"}
	db.Add(j)
	ok, e := db.Claim(j, time.Now())
	if !ok || e != nil {
		t.Fatalf("%v %v", ok, e)
	}
	if db.Cancel(j.ID) == nil {
		t.Fatal("running cancellation accepted")
	}
}

func TestConcurrentFirstOpen(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := Open(dir)
			if err != nil {
				t.Error(err)
				return
			}
			defer db.Close()
			if err = db.Put("init", []byte("ok")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
