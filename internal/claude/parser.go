package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ParseStream reads NDJSON output from `claude -p --output-format
// stream-json` and emits typed Events on the returned channel. The
// channel closes when r returns io.EOF or any non-recoverable read
// error.
//
// One line of CLI output may produce zero, one, or many Events. For
// example a `stream_event` line wrapping a `content_block_delta`
// with a text_delta unwraps to one TextDeltaEvent; a `stream_event`
// wrapping `message_start` produces one MessageStartEvent; an
// `assistant` line (which is a snapshot of the assistant's final
// message) currently produces no Event because the same information
// already came through the deltas. We could revisit if the UI ever
// needs the snapshot for replay.
//
// Errors during JSON parsing are reported on the channel as
// UnknownEvents with the raw line attached, NOT as a separate error
// channel — the runner must keep streaming downstream events even if
// one line is malformed, because the CLI may emit a new field in a
// later version that breaks our struct tags.
func ParseStream(r io.Reader) <-chan Event {
	out := make(chan Event, 64)
	go func() {
		defer close(out)
		// The CLI emits lines that can be very long when stream_event
		// carries a full assistant message snapshot. 1 MiB is enough
		// headroom for v1; we'll lift it if assistants start sending
		// multi-MB rich content.
		const maxLine = 1 << 20
		sc := bufio.NewScanner(r)
		buf := make([]byte, 0, 64*1024)
		sc.Buffer(buf, maxLine)
		for sc.Scan() {
			line := sc.Bytes()
			if len(strings.TrimSpace(string(line))) == 0 {
				continue
			}
			for _, ev := range parseLine(line) {
				out <- ev
			}
		}
	}()
	return out
}

// parseLine inspects one NDJSON line and returns the zero or more
// Events it represents. Pure function; deterministic given the
// input.
func parseLine(line []byte) []Event {
	// Top-level dispatch by the line's "type" field.
	var head struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype,omitempty"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return []Event{{Kind: KindUnknown, Unknown: &UnknownEvent{
			Type:    "parse_error",
			RawLine: append(json.RawMessage(nil), line...),
		}}}
	}

	switch head.Type {
	case "system":
		var e systemLine
		if err := json.Unmarshal(line, &e); err != nil {
			return []Event{unknown(line)}
		}
		return []Event{{Kind: KindSystem, System: &SystemEvent{
			Subtype:   e.Subtype,
			SessionID: e.SessionID,
			Model:     e.Model,
			CWD:       e.CWD,
		}}}

	case "rate_limit_event":
		var e rateLimitLine
		if err := json.Unmarshal(line, &e); err != nil {
			return []Event{unknown(line)}
		}
		return []Event{{Kind: KindRateLimit, RateLimit: &RateLimitEvent{
			Status:         e.RateLimitInfo.Status,
			RateLimitType:  e.RateLimitInfo.RateLimitType,
			OverageStatus:  e.RateLimitInfo.OverageStatus,
			ResetsAtUnix:   e.RateLimitInfo.ResetsAt,
			IsUsingOverage: e.RateLimitInfo.IsUsingOverage,
		}}}

	case "stream_event":
		// Unwrap the nested Anthropic API event. These give us the
		// fine-grained tokens / blocks / usage.
		return parseStreamEvent(line)

	case "assistant":
		// Snapshot of the assistant's message. For text content we
		// already emitted deltas earlier and the frontend has them.
		// What we DO need from here is the assembled tool_use input —
		// the input_json_delta stream is fragmentary and we
		// deliberately don't emit it incrementally, so this is where
		// the full input first appears.
		return parseAssistantToolInputs(line)

	case "user":
		// `user` lines carry tool_result content blocks that the
		// CLI ran on Claude's behalf and is feeding back. We need
		// these for the UI to render the tool's output.
		return parseUserToolResult(line)

	case "result":
		var e ResultEvent
		if err := json.Unmarshal(line, &e); err != nil {
			return []Event{unknown(line)}
		}
		return []Event{{Kind: KindResult, Result: &e}}

	default:
		return []Event{unknown(line)}
	}
}

// parseStreamEvent peels off the `event` field of a stream_event
// line and converts the nested Anthropic API event into one of our
// Events.
func parseStreamEvent(line []byte) []Event {
	var wrap streamEventLine
	if err := json.Unmarshal(line, &wrap); err != nil {
		return []Event{unknown(line)}
	}
	switch wrap.Event.Type {
	case "message_start":
		return []Event{{Kind: KindMessageStart, MessageStart: &MessageStartEvent{
			MessageID: wrap.Event.Message.ID,
			Model:     wrap.Event.Message.Model,
		}}}
	case "content_block_start":
		// content_block_start with type=tool_use means a tool call is
		// starting. text blocks we don't need to announce separately.
		if wrap.Event.ContentBlock != nil && wrap.Event.ContentBlock.Type == "tool_use" {
			return []Event{{Kind: KindToolUseStart, ToolUseStart: &ToolUseStartEvent{
				Index:     wrap.Event.Index,
				ToolUseID: wrap.Event.ContentBlock.ID,
				Name:      wrap.Event.ContentBlock.Name,
			}}}
		}
		return nil
	case "content_block_delta":
		if wrap.Event.Delta == nil {
			return nil
		}
		switch wrap.Event.Delta.Type {
		case "text_delta":
			return []Event{{Kind: KindTextDelta, TextDelta: &TextDeltaEvent{
				Index: wrap.Event.Index,
				Text:  wrap.Event.Delta.Text,
			}}}
		case "input_json_delta":
			// Tool call input arriving as JSON fragments; we don't
			// emit individual deltas to the UI because the user only
			// cares about the final input on the approval card. We
			// could buffer per index and emit a single ToolUseEnd
			// later, but for v1 we rely on the `assistant` snapshot
			// or content_block_stop for the assembled input.
			return nil
		}
		return nil
	case "content_block_stop":
		// We don't currently distinguish text-stop from tool_use-stop
		// here because we don't have the block's type on stop. The
		// UI can finalize on result/message_stop.
		return []Event{{Kind: KindTextBlockEnd, TextBlockEnd: &TextBlockEndEvent{
			Index: wrap.Event.Index,
		}}}
	case "message_delta":
		return []Event{{Kind: KindMessageDelta, MessageDelta: &MessageDeltaEvent{
			StopReason: wrap.Event.Delta.StopReason,
			Usage: MessageUsage{
				InputTokens:              wrap.Event.Usage.InputTokens,
				CacheCreationInputTokens: wrap.Event.Usage.CacheCreationInputTokens,
				CacheReadInputTokens:     wrap.Event.Usage.CacheReadInputTokens,
				OutputTokens:             wrap.Event.Usage.OutputTokens,
			},
		}}}
	case "message_stop":
		return []Event{{Kind: KindMessageStop, MessageStop: &MessageStopEvent{}}}
	}
	return []Event{unknown(line)}
}

// parseUserToolResult extracts tool_result blocks from a `user`
// line. A single line may contain multiple tool_results if Claude
// queued several tools in parallel; we emit one Event per result.
func parseUserToolResult(line []byte) []Event {
	var u userLine
	if err := json.Unmarshal(line, &u); err != nil {
		return []Event{unknown(line)}
	}
	out := make([]Event, 0, len(u.Message.Content))
	for _, c := range u.Message.Content {
		if c.Type != "tool_result" {
			continue
		}
		out = append(out, Event{Kind: KindToolResult, ToolResult: &ToolResultEvent{
			ToolUseID: c.ToolUseID,
			Content:   stringifyToolResultContent(c.Content),
			IsError:   c.IsError,
		}})
	}
	return out
}

// stringifyToolResultContent normalises tool_result.content, which
// may be either a plain string or an array of {type:"text", text:...}
// blocks (the latter when Claude's tool_result composes multiple
// segments).
func stringifyToolResultContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Try array of text blocks.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var sb strings.Builder
		for _, b := range blocks {
			if b.Type == "text" {
				sb.WriteString(b.Text)
			}
		}
		return sb.String()
	}
	// Last resort: dump the raw JSON.
	return string(raw)
}

func unknown(line []byte) Event {
	return Event{Kind: KindUnknown, Unknown: &UnknownEvent{
		Type:    extractTypeBestEffort(line),
		RawLine: append(json.RawMessage(nil), line...),
	}}
}

func extractTypeBestEffort(line []byte) string {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return "parse_error"
	}
	if head.Type == "" {
		return "no_type"
	}
	return head.Type
}

// ---- on-the-wire structs (private; the public surface is Event) ----

type systemLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	CWD       string `json:"cwd"`
}

type rateLimitLine struct {
	RateLimitInfo struct {
		Status         string `json:"status"`
		RateLimitType  string `json:"rateLimitType"`
		OverageStatus  string `json:"overageStatus"`
		ResetsAt       int64  `json:"resetsAt"`
		IsUsingOverage bool   `json:"isUsingOverage"`
	} `json:"rate_limit_info"`
}

type streamEventLine struct {
	Event streamEventInner `json:"event"`
}

type streamEventInner struct {
	Type         string             `json:"type"`
	Index        int                `json:"index"`
	Message      streamEventMessage `json:"message"`
	ContentBlock *contentBlock      `json:"content_block,omitempty"`
	Delta        *streamEventDelta  `json:"delta,omitempty"`
	Usage        streamEventUsage   `json:"usage"`
}

type streamEventMessage struct {
	ID    string `json:"id"`
	Model string `json:"model"`
}

type contentBlock struct {
	Type string `json:"type"`
	ID   string `json:"id"` // only present for tool_use
	Name string `json:"name"`
}

type streamEventDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	StopReason  string `json:"stop_reason"`
	PartialJSON string `json:"partial_json"`
}

type streamEventUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

type userLine struct {
	Message userMessage `json:"message"`
}

type userMessage struct {
	Role    string        `json:"role"`
	Content []userContent `json:"content"`
}

type userContent struct {
	Type      string          `json:"type"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// silence unused import linter; keep fmt available if we need
// debug-printing during development.
var _ = fmt.Sprintf

// parseAssistantToolInputs extracts tool_use blocks (with the fully
// assembled input JSON) from an `assistant` snapshot line and emits
// one ToolUseEnd event per tool. The CLI sends this snapshot AFTER
// the matching content_block_start (ToolUseStart) + input_json_deltas
// have streamed past, so by the time the frontend sees ToolUseEnd
// the tool card is already on screen and only needs its input
// field populated.
func parseAssistantToolInputs(line []byte) []Event {
	var wrap struct {
		Message struct {
			Content []struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Index int             `json:"index"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &wrap); err != nil {
		return nil
	}
	out := make([]Event, 0, len(wrap.Message.Content))
	for _, c := range wrap.Message.Content {
		if c.Type != "tool_use" || c.ID == "" {
			continue
		}
		out = append(out, Event{
			Kind: KindToolUseEnd,
			ToolUseEnd: &ToolUseEndEvent{
				Index:     c.Index,
				ToolUseID: c.ID,
				Name:      c.Name,
				Input:     c.Input,
			},
		})
	}
	return out
}
