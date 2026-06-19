package claudehistory

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
)

// promptInjectionSeparator is the literal string composePromptText
// (in internal/api/claude_handlers.go) writes between the user's
// message and the rendered template body. Splitting on this string
// is how the history rebuild recovers the user-visible vs full
// prompt distinction; if you change one, change both.
const promptInjectionSeparator = "\n\n---\n"

// splitInjectedPrompt returns (userText, expanded) for a prompt
// string read out of jsonl. If the separator is present, userText is
// the text before it and expanded is the full original string. If not,
// userText is the full string and expanded is empty (no toggle to
// show on the frontend).
func splitInjectedPrompt(s string) (userText, expanded string) {
	if i := strings.Index(s, promptInjectionSeparator); i >= 0 {
		return s[:i], s
	}
	return s, ""
}

// Parse reads a Claude CLI jsonl transcript and reconstructs the
// conversation as a slice of Turns (oldest → newest).
//
// Pagination:
//   - limit clamps how many turns are returned. ≤ 0 means "all".
//   - beforeTurnID, if set, returns the last `limit` turns whose IDs
//     come strictly before it in the original sequence. (Frontends can
//     scroll backwards by passing the oldest visible turn's id.)
//
// Robustness: lines that don't unmarshal are logged and skipped. An
// unrecoverable file error (open failure) is returned. Empty files
// return ([]Turn{}, nil). The trailing turn is sealed (Done=true)
// even if Claude was mid-reply — refresh-time reads never observe a
// live stream.
func Parse(path string, limit int, beforeTurnID string) ([]Turn, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// tool_result.content can be hundreds of KB (Read output). Bump
	// the line size cap well above the default 64KB.
	scanner.Buffer(make([]byte, 0, 1<<20), 4<<20)

	var turns []Turn
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var head lineHead
		if err := json.Unmarshal(line, &head); err != nil {
			slog.Warn("claudehistory.Parse: skipping malformed jsonl line",
				"path", path, "err", err)
			continue
		}
		switch head.Type {
		case "user":
			handleUser(&turns, line)
		case "assistant":
			handleAssistant(&turns, line)
		default:
			// silently skip: permission-mode, attachment, ai-title,
			// last-prompt, system, file-history-snapshot, queue-operation,
			// and any future types
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		// Treat as partial-read warning, not a fatal error.
		slog.Warn("claudehistory.Parse: scanner error",
			"path", path, "err", err)
	}

	// Mark all turns done — we never serve mid-stream from jsonl.
	for i := range turns {
		turns[i].Done = true
	}

	return paginate(turns, limit, beforeTurnID), nil
}

// lineHead captures just enough of each line to decide how to dispatch.
type lineHead struct {
	Type    string `json:"type"`
	UUID    string `json:"uuid"`
	TS      string `json:"timestamp"`
	Message rawMsg `json:"message"`
}

type rawMsg struct {
	Content json.RawMessage `json:"content"`
}

// handleUser branches on whether content is a string (new turn) or
// an array (tool_result items resolving the current turn's tools).
func handleUser(turns *[]Turn, line []byte) {
	var full struct {
		UUID    string `json:"uuid"`
		TS      string `json:"timestamp"`
		Message rawMsg `json:"message"`
	}
	if err := json.Unmarshal(line, &full); err != nil {
		return
	}
	c := full.Message.Content
	if len(c) == 0 {
		return
	}
	// Any user-line timestamp brackets the end of the preceding turn:
	// a tool_result row's ts lands mid-turn (turn still in progress
	// against the next assistant message), and a new prompt's ts is
	// the closest hard bracket between the previous turn ending and
	// the next one starting. Both are upper bounds on "when the
	// previous assistant work finished"; the later one wins because
	// we keep overwriting as more rows arrive.
	if full.TS != "" && len(*turns) > 0 {
		(*turns)[len(*turns)-1].EndedAt = full.TS
	}
	// Try string content first.
	if c[0] == '"' {
		var s string
		if err := json.Unmarshal(c, &s); err != nil {
			return
		}
		id := full.UUID
		if id == "" {
			id = stableID(line)
		}
		// Recover the user-visible prompt vs the server-injected
		// expanded prompt by splitting on the "\n\n---\n" separator
		// that composePromptText writes in internal/api/claude_handlers.go.
		// Claude jsonl records only the post-injection text (that's
		// what Claude actually saw), so without this split the
		// rebuilt history shows the user a wall of injected template
		// text under "You" instead of their original message.
		userText, expanded := splitInjectedPrompt(s)
		*turns = append(*turns, Turn{
			ID:             id,
			Prompt:         userText,
			ExpandedPrompt: expanded,
			StartedAt:      full.TS,
			Blocks:         []Block{},
		})
		return
	}
	// Array content — look for tool_result items.
	if c[0] != '[' {
		return
	}
	var items []json.RawMessage
	if err := json.Unmarshal(c, &items); err != nil {
		return
	}
	if len(*turns) == 0 {
		return
	}
	cur := &(*turns)[len(*turns)-1]
	for _, raw := range items {
		var item struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id"`
			Content   string `json:"content"`
			IsError   bool   `json:"is_error"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		if item.Type != "tool_result" {
			continue
		}
		// Match the tool_result back to the matching tool block by
		// toolUseId and patch in the result + error flag. Tool blocks
		// live mixed in with text blocks; we have to walk the array.
		for i := range cur.Blocks {
			if cur.Blocks[i].Kind == "tool" && cur.Blocks[i].Tool != nil && cur.Blocks[i].Tool.ToolUseID == item.ToolUseID {
				cur.Blocks[i].Tool.Result = item.Content
				cur.Blocks[i].Tool.IsError = item.IsError
				break
			}
		}
	}
}

// handleAssistant appends text or tool_use to the current turn.
func handleAssistant(turns *[]Turn, line []byte) {
	var full struct {
		Message struct {
			Content []json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &full); err != nil {
		return
	}
	if len(*turns) == 0 {
		// Orphaned assistant content (no preceding user prompt) —
		// drop it. Spec edge case.
		return
	}
	cur := &(*turns)[len(*turns)-1]
	for _, raw := range full.Message.Content {
		var item struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"`
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		switch item.Type {
		case "text":
			// Append a text block. Don't concat into a previous text
			// block — assistant messages CAN have multiple text
			// content items at top level (rare, but legal), and even
			// if Claude only emits one per message, multiple
			// assistant messages within the same Alfred turn (e.g.
			// the model thinks, calls a tool, replies again) each
			// land as a separate text block.
			if item.Text != "" {
				cur.Blocks = append(cur.Blocks, Block{Kind: "text", Text: item.Text})
			}
		case "thinking":
			// One whole thinking block per content item — append
			// rather than concat so the UI can render each as its
			// own collapsible card.
			if item.Thinking != "" {
				cur.Thinking = append(cur.Thinking, item.Thinking)
			}
		case "tool_use":
			cur.Blocks = append(cur.Blocks, Block{
				Kind: "tool",
				Tool: &ToolCall{
					ToolUseID: item.ID,
					Name:      item.Name,
					Input:     []byte(item.Input),
					Decision:  "allow",
				},
			})
		}
	}
}

// stableID derives a deterministic ID from a line's bytes when the
// jsonl didn't supply a uuid. SHA-1 first 16 hex chars — long enough
// to avoid collisions across a single file's worth of turns.
func stableID(line []byte) string {
	h := sha1.Sum(line)
	return hex.EncodeToString(h[:])[:16]
}

// paginate trims `turns` to the last `limit` entries ending before
// `beforeTurnID` (or just the last `limit` if `beforeTurnID` is empty).
// limit <= 0 means "no limit".
func paginate(turns []Turn, limit int, beforeTurnID string) []Turn {
	if beforeTurnID != "" {
		idx := -1
		for i, t := range turns {
			if t.ID == beforeTurnID {
				idx = i
				break
			}
		}
		if idx >= 0 {
			turns = turns[:idx]
		}
	}
	if limit > 0 && len(turns) > limit {
		turns = turns[len(turns)-limit:]
	}
	return turns
}
