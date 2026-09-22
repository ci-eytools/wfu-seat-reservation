// Package storage keeps each account in a separate, private database.
package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var accountRE = regexp.MustCompile(`^(?:[\p{Han}]{1,4}[0-9]{1,44}|[a-zA-Z0-9][a-zA-Z0-9_-]{0,47})$`)

func AccountDir(root, id string) (string, error) {
	if !accountRE.MatchString(id) {
		return "", errors.New("账号名请使用姓＋学号，例如张20260001（兼容原有英文账号）")
	}
	dir := filepath.Join(root, "accounts", id)
	for _, part := range []string{filepath.Join(root, "accounts"), dir} {
		if info, err := os.Lstat(part); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("账号目录不能是符号链接")
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}
func Accounts(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "accounts"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && accountRE.MatchString(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}

type Store struct{ db *sql.DB }

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "state.db")
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("数据库不能是符号链接")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err = os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec("PRAGMA busy_timeout=5000")
	if err == nil {
		err = retryBusy(func() error {
			var mode string
			if e := db.QueryRow("PRAGMA journal_mode").Scan(&mode); e != nil {
				return e
			}
			if mode != "wal" {
				if _, e := db.Exec("PRAGMA journal_mode=WAL"); e != nil {
					return e
				}
			}
			_, e := db.Exec(` CREATE TABLE IF NOT EXISTS kv(k TEXT PRIMARY KEY,v BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY,payload BLOB NOT NULL,state TEXT NOT NULL,next_ms INTEGER NOT NULL,lease_ms INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS runs(job_id TEXT NOT NULL,at_ms INTEGER NOT NULL,result TEXT NOT NULL,PRIMARY KEY(job_id,at_ms));`)
			return e
		})
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Get(key string) ([]byte, error) {
	var b []byte
	err := s.db.QueryRow("SELECT v FROM kv WHERE k=?", key).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return b, err
}
func (s *Store) Put(key string, b []byte) error {
	_, err := s.db.Exec("INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", key, b)
	return err
}
func (s *Store) Delete(key string) error {
	_, err := s.db.Exec("DELETE FROM kv WHERE k=?", key)
	return err
}

// DayPlan is one weekday's interval; WeekPlan uses Monday through Sunday order.
type DayPlan struct {
	Enabled bool   `json:"enabled"`
	Start   string `json:"start,omitempty"`
	End     string `json:"end,omitempty"`
}
type Job struct {
	RepeatForever bool      `json:"repeat_forever,omitempty"`
	WeekPlan      []DayPlan `json:"week_plan,omitempty"`
	RepeatMode    string    `json:"repeat_mode,omitempty"`

	RepeatStart  string    `json:"repeat_start,omitempty"`
	RepeatUntil  string    `json:"repeat_until,omitempty"`
	SkipWeekends bool      `json:"skip_weekends,omitempty"`
	AwaitWindow  bool      `json:"await_window,omitempty"`
	RoomWindow   bool      `json:"room_window,omitempty"`
	ID           string    `json:"id"`
	RoomID       int       `json:"room_id"`
	Room         string    `json:"room"`
	Day          string    `json:"day"`
	Start        string    `json:"start"`
	End          string    `json:"end"`
	Seats        []string  `json:"seats"`
	Cron         string    `json:"cron,omitempty"`
	DayOffset    int       `json:"day_offset"`
	State        string    `json:"state"`
	Next         time.Time `json:"next"`
	Created      time.Time `json:"created"`
	Result       string    `json:"result,omitempty"`
	GlobalTime   string    `json:"global_time,omitempty"`
}

func (s *Store) Add(j Job) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO jobs(id,payload,state,next_ms) VALUES(?,?,?,?)", j.ID, b, j.State, j.Next.UnixMilli())
	return err
}
func (s *Store) Jobs() ([]Job, error) {
	rows, err := s.db.Query("SELECT payload,state,next_ms FROM jobs ORDER BY next_ms,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		var b []byte
		var state string
		var n int64
		if err = rows.Scan(&b, &state, &n); err != nil {
			return nil, err
		}
		var j Job
		if err = json.Unmarshal(b, &j); err != nil {
			return nil, err
		}
		j.State = state
		j.Next = time.UnixMilli(n)
		out = append(out, j)
	}
	return out, rows.Err()
}

// Claim commits before any network write. Two processes cannot claim the same occurrence.
func (s *Store) Claim(j Job, now time.Time) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	r, err := tx.Exec("UPDATE jobs SET state='running',lease_ms=? WHERE id=? AND state='pending' AND next_ms=? AND NOT EXISTS(SELECT 1 FROM kv WHERE k='disabled' AND v=x'31')", now.Add(5*time.Minute).UnixMilli(), j.ID, j.Next.UnixMilli())
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return false, nil
	}
	_, err = tx.Exec("INSERT INTO runs(job_id,at_ms,result) VALUES(?,?,'running')", j.ID, j.Next.UnixMilli())
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}
func (s *Store) Finish(j Job, occurrence time.Time) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec("UPDATE jobs SET payload=?,state=?,next_ms=?,lease_ms=0 WHERE id=? AND state='running'", b, j.State, j.Next.UnixMilli(), j.ID)
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE runs SET result=? WHERE job_id=? AND at_ms=?", j.Result, j.ID, occurrence.UnixMilli())
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Cancel(id string) error {
	r, err := s.db.Exec("UPDATE jobs SET state='cancelled' WHERE id=? AND state!='running'", id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return errors.New("任务正在执行或已不存在，不能撤销已发出的请求")
	}
	return nil
}
func (s *Store) Reschedule(j Job) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE jobs SET payload=?,next_ms=? WHERE id=? AND state='pending'", b, j.Next.UnixMilli(), j.ID)
	return err
}

// RescheduleOccurrence cannot overwrite a newer occurrence published by another worker.
func (s *Store) RescheduleOccurrence(j Job, expected time.Time) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE jobs SET payload=?,next_ms=?,state=? WHERE id=? AND state='pending' AND next_ms=?", b, j.Next.UnixMilli(), j.State, j.ID, expected.UnixMilli())
	return err
}

// An interrupted write is never retried automatically: its remote outcome is unknown.
func (s *Store) Recover(now time.Time) error {
	_, err := s.db.Exec("UPDATE jobs SET state='unknown' WHERE state='running' AND lease_ms<?", now.UnixMilli())
	return err
}
func (s *Store) History(id string) ([]string, error) {
	rows, err := s.db.Query("SELECT at_ms,result FROM runs WHERE job_id=? ORDER BY at_ms DESC LIMIT 20", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n int64
		var r string
		if err = rows.Scan(&n, &r); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%s %s", time.UnixMilli(n).Format(time.RFC3339), r))
	}
	return out, rows.Err()
}

// WAL mode changes can return SQLITE_BUSY immediately during simultaneous first
// opens, even with busy_timeout. Retry initialization only, never network writes.
func retryBusy(fn func() error) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := fn()
		if err == nil {
			return nil
		}
		var coded interface{ Code() int }
		if !errors.As(err, &coded) || (coded.Code()&255 != 5 && coded.Code()&255 != 6) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *Store) Enabled() bool { b, e := s.Get("disabled"); return e == nil && string(b) != "1" }
func (s *Store) SetEnabled(enabled bool) error {
	v := "1"
	if enabled {
		v = "0"
	}
	return s.Put("disabled", []byte(v))
}

// RenameAccount preserves credentials and jobs; an existing account is never overwritten.
func RenameAccount(root, oldID, newID string) error {
	if !accountRE.MatchString(oldID) || !accountRE.MatchString(newID) {
		return errors.New("账号标识无效")
	}
	old := filepath.Join(root, "accounts", oldID)
	next := filepath.Join(root, "accounts", newID)
	if _, e := os.Lstat(next); e == nil {
		return errors.New("该身份已存在，请切换到已有账号")
	}
	if st, e := os.Lstat(old); e != nil || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("原账号目录不可迁移")
	}
	return os.Rename(old, next)
}

// RemoveAccount refuses active jobs; disabled claims prevent a new job starting during removal.
func RemoveAccount(root, id string) error {
	if !accountRE.MatchString(id) {
		return errors.New("账号标识无效")
	}
	dir := filepath.Join(root, "accounts", id)
	st, e := os.Lstat(dir)
	if e != nil {
		return e
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return errors.New("不能删除符号链接")
	}
	db, e := Open(dir)
	if e != nil {
		return e
	}
	if e = db.SetEnabled(false); e != nil {
		db.Close()
		return e
	}
	jobs, e := db.Jobs()
	if e != nil {
		db.Close()
		return e
	}
	for _, j := range jobs {
		if j.State == "running" {
			db.Close()
			return errors.New("预订仍在执行，请等待结果后删除")
		}
	}
	db.Close()
	return os.RemoveAll(dir)
}

var identifiedAccountRE = regexp.MustCompile(`^[\p{Han}]{1,4}[0-9]{1,44}$`)

func IsIdentifiedAccount(id string) bool { return identifiedAccountRE.MatchString(id) }

var serviceAccountRE = regexp.MustCompile(`^u-[a-f0-9]{40}$`)

func IsServiceAccount(id string) bool { return serviceAccountRE.MatchString(id) }

// ServiceAccountID is derived exclusively from a server-verified school identity.
func ServiceAccountID(school, student string) string {
	sum := sha256.Sum256([]byte(school + ":" + student))
	return fmt.Sprintf("u-%x", sum[:20])
}

// AddOnce durably binds a request key to its content and resulting job.
func (s *Store) AddOnce(key, digest string, j Job) (Job, error) {
	tx, e := s.db.Begin()
	if e != nil {
		return Job{}, e
	}
	defer tx.Rollback()
	var old []byte
	e = tx.QueryRow("SELECT v FROM kv WHERE k=?", "request/"+key).Scan(&old)
	if e == nil {
		var r struct {
			Digest string
			Job    Job
		}
		if json.Unmarshal(old, &r) != nil || r.Digest != digest {
			return Job{}, errors.New("幂等键已用于不同请求")
		}
		return r.Job, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return Job{}, e
	}
	b, e := json.Marshal(j)
	if e != nil {
		return Job{}, e
	}
	if _, e = tx.Exec("INSERT INTO jobs(id,payload,state,next_ms) VALUES(?,?,?,?)", j.ID, b, j.State, j.Next.UnixMilli()); e != nil {
		return Job{}, e
	}
	r, _ := json.Marshal(struct {
		Digest string
		Job    Job
	}{digest, j})
	if _, e = tx.Exec("INSERT INTO kv(k,v) VALUES(?,?)", "request/"+key, r); e != nil {
		return Job{}, e
	}
	return j, tx.Commit()
}
