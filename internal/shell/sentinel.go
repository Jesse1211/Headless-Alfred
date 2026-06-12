package shell

import (
	"bytes"
	"fmt"
	"strconv"
)

// RS is the ASCII Record Separator (0x1e). Used to bracket sentinel lines so
// they're vanishingly unlikely to appear naturally in command output.
const RS = '\x1e'

type EventKind int

const (
	EventStart EventKind = iota
	EventChunk
	EventEnd
)

type ParseEvent struct {
	Kind     EventKind
	CmdID    string
	Cwd      string // only on EventStart
	Bytes    []byte // only on EventChunk
	ExitCode int    // only on EventEnd
}

// Wrap returns the byte sequence to write to bash's stdin so that the user
// command runs sandwiched between START and END sentinels.
//
// Format on START line (sent verbatim to bash):
//
//	printf '\x1eALFRED_START_<nonce> <cmdID> %s\x1eX\n' "$PWD"
//
// On END line:
//
//	printf '\x1eALFRED_END_<nonce> <cmdID> %d\x1eX\n' "$__alfred_ec"
func Wrap(nonce, cmdID, userCmd string) string {
	return fmt.Sprintf(
		"printf '\\x1eALFRED_START_%s %s %%s\\x1eX\\n' \"$PWD\"\n%s\n__alfred_ec=$?\nprintf '\\x1eALFRED_END_%s %s %%d\\x1eX\\n' \"$__alfred_ec\"\n",
		nonce, cmdID, userCmd, nonce, cmdID,
	)
}

// Parser feeds bytes from the PTY and emits events.
type Parser struct {
	nonce   string
	OnEvent func(ParseEvent)

	buf   bytes.Buffer
	state parseState
	cur   activeCmd
}

type parseState int

const (
	stateOutside parseState = iota // bytes belong to no command — discard
	stateInside                    // bytes belong to cur.id — emit as chunks
)

type activeCmd struct {
	id string
}

func NewParser(nonce string) *Parser {
	return &Parser{nonce: nonce, state: stateOutside}
}

// ResumeInside seeds the parser so it treats bytes fed before any
// new START sentinel as body of the given cmdID. Used by TmuxShell.Resume
// when a previous alfred died mid-command: the START sentinel was already
// consumed (past pty.offset), the new reader resumes mid-stream, and
// without this seed the parser would silently drop body bytes in
// stateOutside until the END sentinel arrives.
func (p *Parser) ResumeInside(cmdID string) {
	p.cur = activeCmd{id: cmdID}
	p.state = stateInside
}

// Feed appends bytes from the PTY and processes any complete sentinel lines.
// Bytes that are part of a sentinel are consumed; bytes belonging to the
// current command body are forwarded via OnEvent(EventChunk).
func (p *Parser) Feed(b []byte) {
	p.buf.Write(b)
	p.process()
}

func (p *Parser) process() {
	for {
		data := p.buf.Bytes()
		// Look for next RS.
		idx := bytes.IndexByte(data, RS)
		if idx < 0 {
			// No RS: everything is plain body (or pre-START noise).
			if p.state == stateInside && len(data) > 0 {
				p.emit(EventChunk, p.cur.id, "", append([]byte{}, data...), 0)
			}
			p.buf.Reset()
			return
		}
		// Bytes before RS are body (if inside) or discarded (if outside).
		if idx > 0 {
			if p.state == stateInside {
				p.emit(EventChunk, p.cur.id, "", append([]byte{}, data[:idx]...), 0)
			}
		}
		// Now starting at the RS, look for the closing RS that ends the sentinel.
		rest := data[idx:]
		end := bytes.IndexByte(rest[1:], RS)
		if end < 0 {
			// Sentinel not yet complete; keep RS and what follows in buf, retry on next feed.
			p.buf.Reset()
			p.buf.Write(rest)
			return
		}
		// rest[0..end+1] is the sentinel line including both RS bytes.
		sentinel := string(rest[1 : end+1]) // between the two RS bytes
		// Consume the sentinel plus the "X" terminator char and optional newline.
		consumed := end + 2 // RS .. RS
		// Skip terminator byte ('X') and any \n that follows it.
		tail := rest[consumed:]
		extra := 0
		if len(tail) > 0 && tail[0] == 'X' {
			extra++
		}
		if len(tail) > extra && tail[extra] == '\n' {
			extra++
		}
		p.handleSentinel(sentinel)
		// Rebuild buffer: drop everything up to and including consumed+extra.
		newBuf := append([]byte{}, rest[consumed+extra:]...)
		p.buf.Reset()
		p.buf.Write(newBuf)
	}
}

func (p *Parser) handleSentinel(s string) {
	// Expected forms:
	//   ALFRED_START_<nonce> <cmdID> <cwd>
	//   ALFRED_END_<nonce> <cmdID> <exitCode>
	startPrefix := "ALFRED_START_" + p.nonce + " "
	endPrefix := "ALFRED_END_" + p.nonce + " "
	switch {
	case len(s) > len(startPrefix) && s[:len(startPrefix)] == startPrefix:
		rest := s[len(startPrefix):]
		// rest = "<cmdID> <cwd>"
		sp := bytes.IndexByte([]byte(rest), ' ')
		if sp < 0 {
			return
		}
		id := rest[:sp]
		cwd := rest[sp+1:]
		p.cur = activeCmd{id: id}
		p.state = stateInside
		p.emit(EventStart, id, cwd, nil, 0)
	case len(s) > len(endPrefix) && s[:len(endPrefix)] == endPrefix:
		rest := s[len(endPrefix):]
		sp := bytes.IndexByte([]byte(rest), ' ')
		if sp < 0 {
			return
		}
		id := rest[:sp]
		ec, err := strconv.Atoi(rest[sp+1:])
		if err != nil {
			ec = -1
		}
		p.emit(EventEnd, id, "", nil, ec)
		p.cur = activeCmd{}
		p.state = stateOutside
	}
}

func (p *Parser) emit(k EventKind, id, cwd string, body []byte, ec int) {
	if p.OnEvent == nil {
		return
	}
	p.OnEvent(ParseEvent{Kind: k, CmdID: id, Cwd: cwd, Bytes: body, ExitCode: ec})
}
