package session

import (
	"github.com/jesseliu/headless-alfred/internal/shell"
)

// Shell is the subset of *shell.TmuxShell that the rest of the
// codebase (notably internal/api) needs. Defining it here lets the
// api package depend on session, not on shell, and makes it cheap
// to swap implementations (single-bash fallback, in-test fakes).
type Shell interface {
	Write(cmdID, userCmd string) error
	Stop()
	CurrentCommand() *shell.RunningCommand
	SubscribeEvents(buffer int) (*shell.EventSubscriber, func())
	// Claude-mode APIs.
	SubscribeRaw(buffer int) *shell.Subscriber
	EnterClaude() error
	SendStdin(data []byte) error
	ExitClaude() error
}
