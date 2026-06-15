// Package template owns the server-side registry of prompt
// templates that get appended to user prompts when a session opts
// in via the Start Claude dialog. Templates are server-side
// constants for v1 — there is no UI for the user to author or
// edit them. New built-ins are added to Builtins in code.
package template

// Template is one entry in the registry.
type Template struct {
	// ID is the stable key used in URLs, WS frames, and on disk
	// (SessionMeta.TemplateID). Kebab-case; URL-safe.
	ID string

	// Name is the human-readable label shown in the UI (read-only
	// viewer).
	Name string

	// Content is the raw template text. Render() substitutes the
	// per-session placeholders before it is appended to the user's
	// prompt.
	//
	// Supported placeholders:
	//   <sid>            — the Alfred session ID
	//   <summary_path>   — the absolute path to the session's
	//                      summary file on disk
	Content string
}

// Builtins is the registry. Keys equal Template.ID.
//
// summary-todo: the v1 default. Asks Claude to maintain a short
// task-list summary in the session's summary file. The user reads
// it in the right-hand sidebar; updates happen by Claude's Read +
// Write tools.
var Builtins = map[string]Template{
	"summary-todo": {
		ID:   "summary-todo",
		Name: "Task summary",
		Content: `After your reply, update the session summary at
<summary_path> so we don't lose context across turns.

Steps:

1. Use Read on <summary_path> first if it exists; preserve
   still-relevant content, remove obsolete items.
2. Rewrite the whole file in this shape (keep the WHOLE file
   under 1500 characters — short bullets, no narrative
   paragraphs, no verbatim code blocks):

## Goal
<one line: what we're trying to achieve>

## Status
<one line: in progress / blocked on X / done>

## Decisions
- <terse bullets of what we've agreed on>

## Open questions
- <things still unresolved>

3. Use Write (one tool call, full file contents).

Session id: <sid>.
`,
	},
}
