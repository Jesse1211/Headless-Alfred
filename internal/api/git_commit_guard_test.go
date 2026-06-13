package api

import "testing"

func TestValidateGitCommit(t *testing.T) {
	cases := []struct {
		in   string
		want bool // true = expect error
	}{
		{"git commit", true},
		{"git commit --amend", true},
		{"git commit -m 'fix'", false},
		{"git commit -m fix", false},
		{`git commit -m "feat: x"`, false},
		{"git commit --message=foo", false},
		{"git commit --message foo", false},
		{"git commit -mfix", false}, // joined form
		{"git status", false},
		{"git push", false},
		{"echo git commit", false}, // not invoking git
		{"", false},
		{"ls", false},
	}
	for _, c := range cases {
		got := validateGitCommit(c.in)
		isErr := got != ""
		if isErr != c.want {
			t.Errorf("validateGitCommit(%q) = %q (isErr=%v), want isErr=%v", c.in, got, isErr, c.want)
		}
	}
}
