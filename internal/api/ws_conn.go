package api

// wsConn bundles all the per-WS-connection subscriptions, watchers,
// channels and goroutine plumbing that runClientLoop's select reads
// from. Pulling it out of runClientLoop leaves that function as just
// the on-connect handshake plus the fan-in loop body.
//
// Lifecycle: newWSConn does every subscribe/watch/spawn; close() runs
// every teardown in reverse-ish order. runClientLoop owns a single
// `defer c.close()`. The `cancels` slice is mutable after construction
// — the loop's session-created case appends new unsubscribe funcs to
// it as sessions join mid-connection — so it lives on the struct, not
// captured in a closure.

import (
	"context"
	"log/slog"
	"time"

	"github.com/jesseliu/headless-alfred/internal/claude"
	"github.com/jesseliu/headless-alfred/internal/notes"
	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/store"
	"github.com/jesseliu/headless-alfred/internal/summary"
)

type wsConn struct {
	// connCtx is cancelled on close so bg-task log watcher goroutines
	// exit even without an explicit unsubscribe.
	connCtx    context.Context
	connCancel context.CancelFunc

	// Per-WS bg-task log subscriptions (fsnotify watchers).
	bgLogSubs *bgTaskLogSubs
	bgMeta    MetaResolver
	bgCWD     CWDResolver

	// stop closes on teardown; every forwarder goroutine selects on it.
	stop chan struct{}
	// cancels accumulates subscriber-close funcs. Appended to both at
	// construction and from runClientLoop's session-created case.
	cancels []func()

	// Shared per-connection channels the select loop reads.
	ptyChunks    chan ptyChunk
	events       chan FanInEvent
	asks         chan claude.PendingRequest
	claudeEvents chan claudeEventEnvelope
	closedCh     chan string
	renamedCh    chan namedRename
	createdCh    chan string

	claudeRunStates *claudeRunStateMap

	// Watcher update channels (summaries / notes on disk).
	summaryUpdates chan string
	noteUpdates    chan string

	// Process-wide broadcaster subscriptions.
	recapSub <-chan string
	diskSub  <-chan DiskUsage

	pingTicker *time.Ticker

	// Teardown funcs registered by newWSConn, run in close().
	teardowns []func()
}

// newWSConn wires up every subscription, watcher and forwarder for one
// WS connection. `sessions` is the snapshot of sessions present at
// connect time; their event/raw/ask subscriptions are established here.
func newWSConn(
	m *session.Manager,
	disp *claude.Dispatcher,
	broadcaster *recapBroadcaster,
	disk *diskBroadcaster,
	sessions []store.SessionMeta,
) *wsConn {
	connCtx, connCancel := context.WithCancel(context.Background())
	c := &wsConn{
		connCtx:         connCtx,
		connCancel:      connCancel,
		bgLogSubs:       newBgTaskLogSubs(),
		bgMeta:          NewSessionMetaResolver(m),
		bgCWD:           NewSessionCWDResolver(m),
		stop:            make(chan struct{}),
		ptyChunks:       make(chan ptyChunk, 64),
		events:          make(chan FanInEvent, 64),
		asks:            make(chan claude.PendingRequest, 16),
		claudeEvents:    make(chan claudeEventEnvelope, 64),
		closedCh:        make(chan string, 4),
		renamedCh:       make(chan namedRename, 4),
		createdCh:       make(chan string, 4),
		claudeRunStates: newClaudeRunStateMap(),
		summaryUpdates:  make(chan string, 4),
		noteUpdates:     make(chan string, 4),
		pingTicker:      time.NewTicker(pingInterval),
	}

	// Subscribe each existing session to shell events + raw PTY bytes.
	subs := make([]NamedSubscriber, 0, len(sessions))
	for _, meta := range sessions {
		sh, err := m.Get(meta.ID)
		if err != nil {
			continue
		}
		sub, cancel := sh.SubscribeEvents(16)
		subs = append(subs, NamedSubscriber{SessionID: meta.ID, Sub: sub})
		c.cancels = append(c.cancels, cancel)
		rawSub := sh.SubscribeRaw(64)
		c.cancels = append(c.cancels, rawSub.Close)
		go forwardRaw(meta.ID, rawSub, c.ptyChunks, c.stop)
	}

	// Session lifecycle listeners (close / rename / create).
	removeClose := m.AddCloseListener(func(sid string) {
		select {
		case c.closedCh <- sid:
		default:
		}
	})
	removeRename := m.AddRenameListener(func(sid, name string) {
		select {
		case c.renamedCh <- namedRename{ID: sid, Name: name}:
		default:
		}
	})
	removeCreate := m.AddCreateListener(func(sid string) {
		select {
		case c.createdCh <- sid:
		default:
			slog.Warn("ws: createdCh full, dropping created event", "session", sid)
		}
	})
	c.teardowns = append(c.teardowns, removeClose, removeRename, removeCreate)

	go FanIn(subs, c.events, c.stop)

	// Per-WS fsnotify watcher on <DataDir>/summaries/. On a write
	// event, push a summary_updated frame for the matching session.
	// Failure to start is non-fatal: the sidebar stays stale until
	// the user navigates away and back, but the rest of the app
	// keeps working.
	sw, swErr := summary.StartWatcher(m.DataDir(), func(sid string) {
		select {
		case c.summaryUpdates <- sid:
		case <-c.stop:
		}
	})
	if swErr != nil {
		slog.Warn("ws: summary watcher disabled", "err", swErr)
	} else {
		c.teardowns = append(c.teardowns, sw.Stop)
	}

	noteWatcher, noteErr := notes.StartWatcher(m.DataDir(), func(sid string) {
		select {
		case c.noteUpdates <- sid:
		case <-c.stop:
		}
	})
	if noteErr != nil {
		slog.Warn("notes watcher startup failed; notes UI will be stale", "err", noteErr)
	} else {
		c.teardowns = append(c.teardowns, noteWatcher.Stop)
	}

	// Per-connection subscription to the process-wide recap broadcaster.
	// Receives a date string each time a recap file is written.
	recapSub, recapUnsub := broadcaster.subscribe()
	c.recapSub = recapSub
	c.teardowns = append(c.teardowns, recapUnsub)

	// Per-connection subscription to the disk-usage poller. Pushes the
	// current snapshot immediately (so the UI banner state is correct
	// without waiting one poll interval) and again whenever the alert
	// threshold flips.
	if disk != nil {
		s, unsub := disk.subscribe()
		c.diskSub = s
		c.teardowns = append(c.teardowns, unsub)
	}

	// Subscribe each existing session to the bridge's ask dispatcher.
	// Forwards to the per-WS asks channel.
	if disp != nil {
		for _, meta := range sessions {
			subCh, unsub := disp.SubscribeAsks(meta.ID)
			c.cancels = append(c.cancels, unsub)
			go forwardAsks(subCh, c.asks, c.stop)
		}
	}

	return c
}

// close tears down every resource newWSConn (and the loop) registered.
// Order: stop the ping ticker, cancel the connCtx, signal stop (so
// forwarder goroutines exit), close bg-task log watchers, run watcher/
// broadcaster teardowns, then drain the cancels slice (subscriber
// closes). Safe to call once via runClientLoop's defer.
func (c *wsConn) close() {
	c.pingTicker.Stop()
	c.connCancel()
	close(c.stop)
	c.bgLogSubs.closeAll()
	for _, t := range c.teardowns {
		t()
	}
	for _, cancel := range c.cancels {
		cancel()
	}
}
