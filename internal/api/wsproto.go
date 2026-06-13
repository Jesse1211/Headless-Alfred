package api

// Inbound and Outbound WebSocket message shapes. Exported because the
// e2e test suite reuses them (no point hand-maintaining two copies).

// InMsg is a client → server WS frame.
type InMsg struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID,omitempty"`
	Command   string `json:"command,omitempty"`
}

// OutMsg is a server → client WS frame.
type OutMsg struct {
	Type        string `json:"type"`
	SessionID   string `json:"sessionID,omitempty"`
	CmdID       string `json:"cmdId,omitempty"`
	Command     string `json:"command,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	OutputSoFar string `json:"outputSoFar,omitempty"`
	Data        string `json:"data,omitempty"`
	ExitCode    int    `json:"exitCode,omitempty"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	Name        string `json:"name,omitempty"`
	Code        string `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`
}
