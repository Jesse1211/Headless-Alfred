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
	Command         string     `json:"command"`
	Cwd             string     `json:"cwd"`
	StartedAt       time.Time  `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	ExitCode        *int       `json:"exit_code,omitempty"`
	OutputPath      string     `json:"output_path,omitempty"`
	OutputTruncated bool       `json:"output_truncated"`
	Status          Status     `json:"status"`
}
