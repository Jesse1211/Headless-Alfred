package shell

import (
	"bytes"
	"testing"
)

func TestSentinelParser_PassesThroughCommandBody(t *testing.T) {
	p := NewParser("NONCE")
	var events []ParseEvent
	p.OnEvent = func(e ParseEvent) { events = append(events, e) }

	input := []byte("\x1eALFRED_START_NONCE 01HAB /home/user\x1eX\nhello world\n\x1eALFRED_END_NONCE 01HAB 0\x1eX\n")
	p.Feed(input)

	if len(events) < 3 {
		t.Fatalf("want at least 3 events (start, chunk, end), got %d: %+v", len(events), events)
	}
	if events[0].Kind != EventStart || events[0].CmdID != "01HAB" || events[0].Cwd != "/home/user" {
		t.Fatalf("bad start event: %+v", events[0])
	}
	// Find the chunk event(s) and concatenate.
	var body bytes.Buffer
	for _, e := range events {
		if e.Kind == EventChunk {
			body.Write(e.Bytes)
		}
	}
	if body.String() != "hello world\n" {
		t.Fatalf("body=%q want %q", body.String(), "hello world\n")
	}
	last := events[len(events)-1]
	if last.Kind != EventEnd || last.CmdID != "01HAB" || last.ExitCode != 0 {
		t.Fatalf("bad end event: %+v", last)
	}
}

func TestSentinelParser_HandlesSentinelSplitAcrossFeeds(t *testing.T) {
	p := NewParser("NONCE")
	var events []ParseEvent
	p.OnEvent = func(e ParseEvent) { events = append(events, e) }

	input := []byte("\x1eALFRED_START_NONCE 01HAB /tmp\x1eX\nx\n\x1eALFRED_END_NONCE 01HAB 7\x1eX\n")
	// Feed one byte at a time — parser must buffer partial sentinels.
	for i := range input {
		p.Feed(input[i : i+1])
	}

	if events[0].Kind != EventStart {
		t.Fatalf("want start first, got %+v", events[0])
	}
	last := events[len(events)-1]
	if last.Kind != EventEnd || last.ExitCode != 7 {
		t.Fatalf("bad end event: %+v", last)
	}
}

func TestSentinelParser_ExitCodeNonZero(t *testing.T) {
	p := NewParser("NONCE")
	var endEvt ParseEvent
	p.OnEvent = func(e ParseEvent) {
		if e.Kind == EventEnd {
			endEvt = e
		}
	}
	p.Feed([]byte("\x1eALFRED_START_NONCE A /\x1eX\n\x1eALFRED_END_NONCE A 137\x1eX\n"))
	if endEvt.ExitCode != 137 {
		t.Fatalf("exit code = %d, want 137", endEvt.ExitCode)
	}
}

func TestSentinelParser_IgnoresBytesOutsideCommand(t *testing.T) {
	p := NewParser("NONCE")
	var chunks [][]byte
	p.OnEvent = func(e ParseEvent) {
		if e.Kind == EventChunk {
			chunks = append(chunks, append([]byte{}, e.Bytes...))
		}
	}
	// Bytes before any START must be dropped (this is bash startup noise).
	p.Feed([]byte("bash welcome banner\n"))
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks before START, got %d", len(chunks))
	}
}

func TestWrap_ContainsBothSentinels(t *testing.T) {
	wrapped := Wrap("NONCE", "01HAB", "ls -la")
	if !bytes.Contains([]byte(wrapped), []byte("ALFRED_START_NONCE 01HAB")) {
		t.Fatalf("missing start sentinel: %q", wrapped)
	}
	if !bytes.Contains([]byte(wrapped), []byte("ALFRED_END_NONCE 01HAB")) {
		t.Fatalf("missing end sentinel: %q", wrapped)
	}
	if !bytes.Contains([]byte(wrapped), []byte("ls -la")) {
		t.Fatalf("missing user command: %q", wrapped)
	}
}
