// Package api defines the versioned, credential-free client/server contract.
package api

import (
	"time"
	"wfuseat/internal/storage"
)

const Version = "1"

type Identity struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	School      string `json:"school"`
	Student     string `json:"student"`
}
type Settings struct {
	AllowSubmit bool `json:"allow_submit"`
	DelayMS     int  `json:"delay_ms"`
}
type Login struct {
	ID       string    `json:"id"`
	Secret   string    `json:"secret,omitempty"`
	PNG      []byte    `json:"png,omitempty"`
	State    string    `json:"state"`
	Token    string    `json:"token,omitempty"`
	Identity Identity  `json:"identity"`
	Expires  time.Time `json:"expires"`
}
type Me struct {
	Identity    Identity `json:"identity"`
	Settings    Settings `json:"settings"`
	WorkerError string   `json:"worker_error,omitempty"`
}
type Jobs struct {
	Items  []storage.Job  `json:"items"`
	Delays map[string]int `json:"delays"`
}
type JobDraft struct {
	RoomID   int               `json:"room_id"`
	Day      string            `json:"day"`
	Start    string            `json:"start"`
	End      string            `json:"end"`
	Seats    []string          `json:"seats"`
	Mode     string            `json:"mode,omitempty"`
	WeekPlan []storage.DayPlan `json:"week_plan,omitempty"`
}
type Problem struct {
	Error string `json:"error"`
}
