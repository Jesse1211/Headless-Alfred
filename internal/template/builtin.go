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
	//   <date>           — today's local date (YYYY-MM-DD)
	//   <cwd>            — the claude invocation cwd
	//   <recap_path>     — the absolute path to today's recap file
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
	"recap-daily": {
		ID:   "recap-daily",
		Name: "Daily recap",
		Content: `Generate today's daily recap for the user. Today is <date>.

Steps:

1. Before doing anything, check whether any superpowers skills apply
   (e.g. a 'daily-recap' or 'summarize' skill). If one does, invoke
   it and follow its instructions instead of these steps.

2. Otherwise, gather data IN PARALLEL (single response with multiple
   tool calls):
   - Bash: cd <cwd> && git log --since="<date> 00:00" --until="<date> 23:59" --all --pretty=format:"%h %s (%an)"
     plus git diff --shortstat HEAD@{midnight}..HEAD
   - If a claude-mem timeline tool is available, call it for today's slice.
   - If a claude-mem memory_search tool is available, query "today's decisions".

   If the claude-mem tools are not available (the user hasn't
   installed the plugin), skip those calls — git alone is enough to
   produce a useful recap.

3. Synthesize a markdown recap with this exact structure:

   # Recap · <date>

   ## Shipped
   - <bullet of concrete output: PR opened, file written, deploy>

   ## Decisions
   - <bullet of judgement calls made, with the why>

   ## Open questions
   - <bullet of unresolved items the user should address tomorrow>

   ## Notes
   - <anything else worth remembering>

4. Write the result to <recap_path> (overwriting any existing file).
   Use the Write tool; do NOT print the recap inline.

5. Confirm to the user with one short line: "Recap saved to <date>.md.
   Ask me about anything from today."
`,
	},
}
