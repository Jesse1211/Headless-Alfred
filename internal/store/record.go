package store

import "time"

type Status string

const (
	StatusRunning     Status = "running"
	StatusCompleted   Status = "completed"
	StatusInterrupted Status = "interrupted"
	StatusStopped     Status = "stopped"
)

type Record struct {
	ID              string     `json:"id"`
	SessionID       string     `json:"session_id"`
	Command         string     `json:"command"`
	Cwd             string     `json:"cwd"`
	StartedAt       time.Time  `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	ExitCode        *int       `json:"exit_code,omitempty"`
	OutputTruncated bool       `json:"output_truncated"`
	Status          Status     `json:"status"`
	// Note: there is intentionally no OutputPath field. The path of the
	// output log is deterministic — Store derives it from the ID. Storing
	// it in the record would duplicate state and invite lost-update bugs
	// (caller modifies record, calls WriteOutput which also modifies record,
	// caller saves, last writer wins).
}
