package api

// Inbound and Outbound WebSocket message shapes. Exported because the
// e2e test suite reuses them (no point hand-maintaining two copies).

// InMsg is a client → server WS frame.
//
// New types added in the Claude-mode rollout:
//   - "enter_claude":  user clicked /claude → ask server to spawn `claude`
//   - "exit_claude":   user clicked Exit Claude → server sends Ctrl+C / Ctrl+D
//   - "stdin":         raw keystrokes for Claude's PTY (Data is base64)
type InMsg struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID,omitempty"`
	Command   string `json:"command,omitempty"`
	Data      string `json:"data,omitempty"` // base64-encoded raw stdin bytes (stdin frame)
}

// OutMsg is a server → client WS frame.
//
// New types added in the Claude-mode rollout:
//   - "claude_entered": mode flipped to claude — frontend mount xterm
//   - "claude_exited":  mode flipped back to shell — frontend mount ChatStream
//   - "pty_data":       raw PTY bytes for xterm.write (claude mode)
//   - "mode" field added to idle / reattach so a reconnecting client
//     knows which view to mount without a race.
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
	Mode        string `json:"mode,omitempty"` // "shell" | "claude" (on idle/reattach)
}
