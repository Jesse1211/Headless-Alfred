package tmuxio

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// ParserSink is the minimal interface StreamReader needs to deliver bytes
// to a sentinel parser. The production parser lives in
// internal/shell.Parser; tests use a recorder.
type ParserSink interface {
	Feed(b []byte)
}

// StreamReader consumes a regular file that tmux pipe-pane appends to,
// hands each chunk to a ParserSink, and persists its byte offset so a
// crash + restart resumes from exactly where it left off.
//
// Not safe for concurrent use; call ReadOnce / TruncateConsumed from a
// single goroutine.
type StreamReader struct {
	streamPath string
	offsetPath string
	sink       ParserSink

	file   *os.File
	offset int64
}

// NewStreamReader opens streamPath (creating it if missing), seeks to
// the offset recorded in offsetPath (or 0 if missing), and returns the
// reader ready for ReadOnce.
//
// streamPath and offsetPath are created with 0o600 perms if absent.
func NewStreamReader(streamPath, offsetPath string, sink ParserSink) (*StreamReader, error) {
	// Make sure the stream file exists.
	if _, err := os.Stat(streamPath); os.IsNotExist(err) {
		f, err := os.OpenFile(streamPath, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create stream: %w", err)
		}
		_ = f.Close()
	} else if err != nil {
		return nil, fmt.Errorf("stat stream: %w", err)
	}

	// Load offset.
	var off int64
	if data, err := os.ReadFile(offsetPath); err == nil {
		parsed, perr := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		if perr != nil {
			return nil, fmt.Errorf("parse offset %q: %w", data, perr)
		}
		off = parsed
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read offset: %w", err)
	}

	f, err := os.OpenFile(streamPath, os.O_RDONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	if off > 0 {
		// Guard against the offset being beyond EOF (e.g., file was
		// truncated externally and offset is stale). Cap at file size.
		info, _ := f.Stat()
		if off > info.Size() {
			off = info.Size()
		}
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("seek to %d: %w", off, err)
		}
	}
	return &StreamReader{
		streamPath: streamPath,
		offsetPath: offsetPath,
		sink:       sink,
		file:       f,
		offset:     off,
	}, nil
}

// Close releases the underlying file descriptor. Idempotent.
func (s *StreamReader) Close() error {
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// Offset returns the current consumed byte position.
func (s *StreamReader) Offset() int64 { return s.offset }

// ReadOnce reads any bytes newly appended since the last call, feeds
// them to the sink, and persists the new offset to disk. Returns the
// number of bytes read (0 means no new data).
func (s *StreamReader) ReadOnce() (int, error) {
	if s.file == nil {
		return 0, fmt.Errorf("stream reader is closed or in error state")
	}
	buf := make([]byte, 32*1024)
	total := 0
	for {
		n, err := s.file.Read(buf)
		if n > 0 {
			s.sink.Feed(buf[:n])
			total += n
			s.offset += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return total, fmt.Errorf("read stream: %w", err)
		}
		if n < len(buf) {
			break
		}
	}
	if total > 0 {
		if err := s.persistOffset(); err != nil {
			return total, err
		}
	}
	return total, nil
}

// TruncateConsumed performs the full safe-truncate dance documented in
// spec §4.4: stop the pipe so tmux releases the file → truncate in
// place → reopen our reader → restart the pipe with pipeCmd.
//
// This MUST be called only when the parser is at an idle boundary
// (between commands). Any bytes bash emits in the ~50ms window
// between StopPipe and RestartPipe are lost; acceptable for a
// single-user tool, documented in the spec.
//
// The runner + session form a tight pair because no other caller can
// sensibly sequence this — if pipe-restart fails the next ReadOnce
// would see zero bytes forever. Keeping it in one method makes the
// contract impossible to mis-sequence.
func (s *StreamReader) TruncateConsumed(runner TmuxRunner, session, pipeCmd string) error {
	// 1. Stop the pipe so tmux closes its write fd.
	if err := runner.PipePane(session, ""); err != nil {
		return fmt.Errorf("stop pipe: %w", err)
	}
	// 2. Close-and-truncate. We MUST close before truncate so the
	//    kernel can shrink the file. If Close errors, we still try to
	//    truncate — the fd is at least leaked, not catastrophic.
	closeErr := s.file.Close()
	s.file = nil
	if err := os.Truncate(s.streamPath, 0); err != nil {
		// Best-effort: try to restore a working reader so subsequent
		// ReadOnce calls fail with the read error rather than panicking.
		if f, oerr := os.OpenFile(s.streamPath, os.O_RDONLY, 0o600); oerr == nil {
			s.file = f
		}
		return fmt.Errorf("truncate: %w (close err: %v)", err, closeErr)
	}
	f, err := os.OpenFile(s.streamPath, os.O_RDONLY, 0o600)
	if err != nil {
		return fmt.Errorf("reopen after truncate: %w (close err: %v)", err, closeErr)
	}
	s.file = f
	s.offset = 0
	if err := s.persistOffset(); err != nil {
		return err
	}
	// 3. Restart the pipe; subsequent bash output flows again.
	if err := runner.PipePane(session, pipeCmd); err != nil {
		return fmt.Errorf("restart pipe: %w", err)
	}
	return nil
}

func (s *StreamReader) persistOffset() error {
	tmp := s.offsetPath + ".tmp"
	data := []byte(strconv.FormatInt(s.offset, 10))
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write offset tmp: %w", err)
	}
	if err := os.Rename(tmp, s.offsetPath); err != nil {
		return fmt.Errorf("rename offset: %w", err)
	}
	return nil
}
