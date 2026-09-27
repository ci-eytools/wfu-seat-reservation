package storage

import (
	"encoding/json"
	"time"
)

type RunOutcome struct {
	Job          Job
	State        string
	Message      string
	At, Finished time.Time
}

func (s *Store) Outcomes(since time.Time) ([]RunOutcome, error) {
	rows, e := s.db.Query("SELECT o.payload,o.state,o.at_ms,o.finished_ms,COALESCE(r.result,'') FROM outcomes o LEFT JOIN runs r ON r.job_id=o.job_id AND r.at_ms=o.at_ms WHERE o.at_ms>=? ORDER BY o.at_ms,o.job_id", since.UnixMilli())
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []RunOutcome
	for rows.Next() {
		var v RunOutcome
		var b []byte
		var at, finished int64
		if e = rows.Scan(&b, &v.State, &at, &finished, &v.Message); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &v.Job); e != nil {
			return nil, e
		}
		v.At = time.UnixMilli(at)
		v.Finished = time.UnixMilli(finished)
		out = append(out, v)
	}
	return out, rows.Err()
}

// The committed sending state is never reclaimed automatically: after a crash,
// Telegram may already have received the message. Only explicit rate limits retry.
func (s *Store) ClaimDelivery(day string, now time.Time) (bool, error) {
	r, e := s.db.Exec("INSERT INTO deliveries(day,state) VALUES(?,'sending') ON CONFLICT(day) DO UPDATE SET state='sending' WHERE deliveries.state='retry' AND deliveries.next_ms<=?", day, now.UnixMilli())
	if e != nil {
		return false, e
	}
	n, e := r.RowsAffected()
	return n == 1, e
}
func (s *Store) FinishDelivery(day, state, note string, next time.Time) error {
	_, e := s.db.Exec("UPDATE deliveries SET state=?,note=?,next_ms=? WHERE day=? AND state='sending'", state, note, next.UnixMilli(), day)
	return e
}
func (s *Store) DeliveryStatus() (string, error) {
	var day, state, note string
	rows, e := s.db.Query("SELECT day,state,note FROM deliveries ORDER BY day DESC LIMIT 1")
	if e != nil {
		return "", e
	}
	defer rows.Close()
	if !rows.Next() {
		return "等待每日汇总", rows.Err()
	}
	if e = rows.Scan(&day, &state, &note); e != nil {
		return "", e
	}
	if state == "sending" {
		note = "正在发送或上次发送中断，请核实聊天记录"
	}
	return day + " · " + note, nil
}

// FreezeBatch preserves the exact multi-page report across retries and restarts.
func (s *Store) FreezeBatch(key string, payload []byte, at time.Time) error {
	_, e := s.db.Exec("INSERT OR IGNORE INTO notification_batches(k,payload,at_ms) VALUES(?,?,?)", key, payload, at.UnixMilli())
	return e
}
func (s *Store) Batches(prefix string, since time.Time) (map[string][]byte, error) {
	rows, e := s.db.Query("SELECT k,payload FROM notification_batches WHERE k LIKE ? AND at_ms>=? ORDER BY k", prefix+"%", since.UnixMilli())
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var k string
		var b []byte
		if e = rows.Scan(&k, &b); e != nil {
			return nil, e
		}
		out[k] = b
	}
	return out, rows.Err()
}

func (s *Store) DeliveryRetryAfter(prefix string) (time.Time, error) {
	var ms int64
	e := s.db.QueryRow("SELECT COALESCE(MAX(next_ms),0) FROM deliveries WHERE day LIKE ? AND state='retry'", prefix+"%").Scan(&ms)
	return time.UnixMilli(ms), e
}
