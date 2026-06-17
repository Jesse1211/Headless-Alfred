package api

import "testing"

func TestVersionAllowed(t *testing.T) {
	good := []string{"latest", "next", "2.1.142", "0.0.1", "10.20.30"}
	bad := []string{
		"",
		"latest; rm -rf /",
		"file:.",
		"github:foo/bar",
		"^2.1.0",
		"2.1",
		"2.1.142-beta",
		"2.1.142 ",
		" 2.1.142",
		"2.1.142.0",
		"@anthropic-ai/claude-code@2.1.142",
	}
	for _, v := range good {
		if !versionAllowed.MatchString(v) {
			t.Errorf("want %q allowed, got rejected", v)
		}
	}
	for _, v := range bad {
		if versionAllowed.MatchString(v) {
			t.Errorf("want %q rejected, got allowed", v)
		}
	}
}
