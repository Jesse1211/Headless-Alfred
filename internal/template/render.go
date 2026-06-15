package template

import "strings"

// RenderArgs is the bag of placeholder values for Render. All fields
// are optional — only the ones a given template uses matter. Adding
// a new placeholder = adding a new field here; old templates ignore it.
type RenderArgs struct {
	SessionID   string // <sid>
	SummaryPath string // <summary_path>
	Date        string // <date>
	Cwd         string // <cwd>
	RecapPath   string // <recap_path>
}

// Render substitutes the named placeholders in the template's Content
// and returns the result. Returns "" for unknown or empty id —
// callers treat that as "no template, skip injection".
func Render(id string, args RenderArgs) string {
	t, ok := Builtins[id]
	if !ok {
		return ""
	}
	s := t.Content
	s = strings.ReplaceAll(s, "<sid>", args.SessionID)
	s = strings.ReplaceAll(s, "<summary_path>", args.SummaryPath)
	s = strings.ReplaceAll(s, "<date>", args.Date)
	s = strings.ReplaceAll(s, "<cwd>", args.Cwd)
	s = strings.ReplaceAll(s, "<recap_path>", args.RecapPath)
	return s
}
