package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// SessionMode is the operating mode of a session.
type SessionMode string

const (
	// ModeShell is the default mode: chat-stream rendering, wrapped commands.
	ModeShell SessionMode = "shell"
	// ModeClaude means the session is talking to the `claude` CLI.
	// The Renderer field of SessionMeta selects how the conversation
	// is presented to the user.
	ModeClaude SessionMode = "claude"
)

// ClaudeRenderer selects how Claude is presented when Mode == ModeClaude.
//
//   - RendererTUI: the legacy xterm.js view, raw PTY bytes from an
//     interactive `claude` invocation. This is what V0 shipped.
//   - RendererUI: a Markdown chat rendered by React. Each user prompt
//     forks a separate `claude -p --resume <uuid> --output-format
//     stream-json` invocation; Alfred intercepts tool use via a hook.
//
// Empty means "session is not in claude mode" (or, for backward
// compatibility with sessions persisted before this field existed,
// the renderer defaulted to TUI).
type ClaudeRenderer string

const (
	RendererTUI ClaudeRenderer = "tui"
	RendererUI  ClaudeRenderer = "ui"
)

// SessionMeta is the persistent metadata for one session. Fields are kept
// minimal on purpose; runtime state (is bash alive, current command) is
// not stored here — that lives in the in-memory session.Manager.
type SessionMeta struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	CreatedAt time.Time   `json:"created_at"`
	Mode      SessionMode `json:"mode,omitempty"` // empty in old files == shell

	// Renderer is only meaningful when Mode == ModeClaude.
	// Empty otherwise. On Pod restart, this is reset to "" by
	// Manager.Reconcile because the tmux pane + claude process are
	// gone; the user re-picks the renderer on re-entry.
	Renderer ClaudeRenderer `json:"renderer,omitempty"`

	// ClaudeSessionID is the Claude CLI conversation UUID. Persists
	// across Mode transitions so re-entering Claude (in either
	// renderer) resumes the same conversation via --resume. Set the
	// first time the session enters Claude mode and never cleared
	// until the Alfred session itself is deleted.
	ClaudeSessionID string `json:"claude_session_id,omitempty"`
}

// SessionsFile is the persisted list of sessions. The file lives at
// <dir>/sessions.json and is read+written as a whole list under an
// atomic tmp+rename. There is no per-entry locking; all writes flow
// through one goroutine (the session.Manager owns the only writer).
type SessionsFile struct {
	dir string
}

func NewSessionsFile(dir string) *SessionsFile {
	return &SessionsFile{dir: dir}
}

func (sf *SessionsFile) path() string {
	return filepath.Join(sf.dir, "sessions.json")
}

// Load returns the persisted list. Returns (nil, nil) if the file
// doesn't exist yet (first boot, never written).
func (sf *SessionsFile) Load() ([]SessionMeta, error) {
	data, err := os.ReadFile(sf.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []SessionMeta
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	// Normalise: older sessions.json files without a "mode" field (or
	// written with omitempty when mode was shell) will have Mode == "".
	// Treat that as ModeShell so callers can always compare without
	// checking for the empty string.
	for i := range list {
		if list[i].Mode == "" {
			list[i].Mode = ModeShell
		}
	}
	return list, nil
}

// Save writes the list atomically. Empty list writes an empty JSON
// array (`[]`), not deletes the file — keeps Load's "missing means
// never written" signal meaningful.
func (sf *SessionsFile) Save(list []SessionMeta) error {
	if list == nil {
		list = []SessionMeta{}
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	final := sf.path()
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	// Best-effort cleanup. If Rename succeeds, tmp no longer exists
	// (rename atomically swaps the inode) so Remove is a harmless
	// noop. If Rename fails, this prevents a stranded .tmp file.
	defer os.Remove(tmp)
	return os.Rename(tmp, final)
}
