package template

import "strings"

// Render substitutes the per-session placeholders in the template
// and returns the result. Returns "" for unknown or empty id —
// callers treat that as "no template, skip injection".
func Render(id, sessionID, summaryPath string) string {
	t, ok := Builtins[id]
	if !ok {
		return ""
	}
	s := t.Content
	s = strings.ReplaceAll(s, "<sid>", sessionID)
	s = strings.ReplaceAll(s, "<summary_path>", summaryPath)
	return s
}
