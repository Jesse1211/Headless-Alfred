package claudestate

import "context"

// Persister is the per-session goroutine that owns the snapshot file.
// Real implementation lands in Task 9; this stub exists so SessionState
// can hold a *Persister field and compile.
type Persister struct{}

// Close is a no-op for the stub. Task 9 implements the full flush.
func (p *Persister) Close(ctx context.Context) error { return nil }
