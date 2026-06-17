package api

import (
	"log/slog"
	"sync"
	"time"
)

// diskBroadcaster polls the PVC's disk usage on a fixed interval
// and pushes updates to every WS subscriber. We push when:
//
//   - The reading crosses one of the alert thresholds (warning at
//     80%, critical at 95%) in either direction. Going up the user
//     sees a new banner; going down the banner clears.
//   - First reading after a subscriber connects (so the UI doesn't
//     start blank waiting up to one poll interval).
//
// Steady-state polls with no threshold crossing do NOT broadcast —
// keeps WS traffic at one frame per minute per session-change,
// not one per minute per client per minute.
//
// One goroutine, owned by main(); subscribers are per-WS-connection.
// Same fan-out shape as recapBroadcaster.
type diskBroadcaster struct {
	dataDir    string
	quotaBytes uint64

	mu   sync.Mutex
	subs map[chan DiskUsage]struct{}
	last DiskUsage
	have bool // last is meaningful (poller has run at least once)

	stop chan struct{}
}

// newDiskBroadcaster starts the poller. interval governs how often
// we walk the Alfred-owned dirs; 60s is a sensible default (jsonl /
// pty.stream growth is bursty but never per-second, and the walk is
// fast enough — thousands of small files in microseconds). Cancel
// via Close().
//
// quotaBytes is the PVC quota the alert thresholds are computed
// against. 0 means "unknown" (env var missing) and we skip
// percentage-based alerts; the snapshot still includes used bytes
// so the UI can show *something*.
func newDiskBroadcaster(dataDir string, quotaBytes uint64, interval time.Duration) *diskBroadcaster {
	b := &diskBroadcaster{
		dataDir:    dataDir,
		quotaBytes: quotaBytes,
		subs:       map[chan DiskUsage]struct{}{},
		stop:       make(chan struct{}),
	}
	go b.loop(interval)
	return b
}

// Close stops the poller. Idempotent — safe to call from multiple
// goroutines and multiple times.
func (b *diskBroadcaster) Close() {
	defer func() { _ = recover() }() // close(closed) is fine
	close(b.stop)
}

func (b *diskBroadcaster) loop(interval time.Duration) {
	// One sample immediately so subscribers connecting before the
	// first tick still get something fresh from currentSnapshot().
	b.poll()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-b.stop:
			return
		case <-t.C:
			b.poll()
		}
	}
}

// poll runs one statfs and broadcasts if the alert threshold
// classification changed (or this is the first reading ever).
func (b *diskBroadcaster) poll() {
	du, err := readDiskUsage(b.dataDir, b.quotaBytes)
	if err != nil {
		slog.Warn("disk poll failed", "err", err, "path", b.dataDir)
		return
	}
	b.mu.Lock()
	first := !b.have
	prev := b.last
	b.last = du
	b.have = true
	shouldBroadcast := first || diskThresholdChanged(prev.UsedPercent, du.UsedPercent)
	if !shouldBroadcast {
		b.mu.Unlock()
		return
	}
	// Snapshot subscribers under the lock; send outside to avoid
	// blocking the poller on a slow consumer (we drop on full).
	chans := make([]chan DiskUsage, 0, len(b.subs))
	for ch := range b.subs {
		chans = append(chans, ch)
	}
	b.mu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- du:
		default:
			// subscriber backed up; next threshold crossing will
			// re-broadcast, no need to retry.
		}
	}
}

// subscribe returns a channel of DiskUsage updates plus an
// unsubscribe func. The channel receives the latest snapshot
// immediately (if available) so the UI can render the banner
// without waiting for the next threshold crossing.
func (b *diskBroadcaster) subscribe() (chan DiskUsage, func()) {
	ch := make(chan DiskUsage, 4)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	if b.have {
		select {
		case ch <- b.last:
		default:
		}
	}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
		close(ch)
	}
}

// diskThresholdChanged returns true if the (warning, critical)
// classification of the percentage changed between prev and curr.
// We broadcast on classification changes only, not every tick, to
// keep WS traffic minimal during steady state.
func diskThresholdChanged(prev, curr float64) bool {
	return diskAlertLevel(prev) != diskAlertLevel(curr)
}

// diskAlertLevel returns 0=ok, 1=warning, 2=critical for a given
// used-percent. Frontend mirrors these as 'ok' / 'warning' /
// 'critical'.
func diskAlertLevel(percent float64) int {
	switch {
	case percent >= 95:
		return 2
	case percent >= 80:
		return 1
	default:
		return 0
	}
}
