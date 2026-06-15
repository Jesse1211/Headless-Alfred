package claude

import (
	"encoding/json"
	"strings"
	"testing"
)

func mkReq(t *testing.T, tool, filePath, content string) PendingRequest {
	t.Helper()
	in := map[string]any{"file_path": filePath}
	if content != "" {
		in["content"] = content
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return PendingRequest{ToolName: tool, ToolInput: raw}
}

func TestIsSummaryIO_AllowsReadOfCanonicalPath(t *testing.T) {
	r := mkReq(t, "Read", "/data/summaries/01SID.md", "")
	if !isSummaryIO(r, "01SID", "/data") {
		t.Error("Read of canonical path should be auto-allow eligible")
	}
}

func TestIsSummaryIO_AllowsSmallWriteOfCanonicalPath(t *testing.T) {
	r := mkReq(t, "Write", "/data/summaries/01SID.md", "## Goal\nfoo")
	if !isSummaryIO(r, "01SID", "/data") {
		t.Error("small Write of canonical path should be auto-allow eligible")
	}
}

func TestIsSummaryIO_RejectsOversizedWrite(t *testing.T) {
	huge := strings.Repeat("x", 9*1024) // 9 KB > 8 KB cap
	r := mkReq(t, "Write", "/data/summaries/01SID.md", huge)
	if isSummaryIO(r, "01SID", "/data") {
		t.Error("oversized Write must fall through to the normal approval path")
	}
}

func TestIsSummaryIO_RejectsOtherPaths(t *testing.T) {
	for _, p := range []string{
		"/data/summaries/OTHER.md",          // wrong sid
		"/data/summaries/01SID.md.bak",      // suffix
		"/data/summaries/../sessions.json",  // path traversal
		"/etc/passwd",                       // wildly unrelated
		"/data/summaries/01SID.md/extra",    // subpath
	} {
		r := mkReq(t, "Write", p, "x")
		if isSummaryIO(r, "01SID", "/data") {
			t.Errorf("path %q must not be auto-allow eligible", p)
		}
	}
}

func TestIsSummaryIO_RejectsOtherTools(t *testing.T) {
	for _, tool := range []string{"Bash", "Edit", "Glob", "Grep"} {
		r := mkReq(t, tool, "/data/summaries/01SID.md", "x")
		if isSummaryIO(r, "01SID", "/data") {
			t.Errorf("tool %q must not be auto-allow eligible", tool)
		}
	}
}

func TestIsSummaryIO_RejectsMalformedInput(t *testing.T) {
	r := PendingRequest{ToolName: "Write", ToolInput: []byte(`not-json`)}
	if isSummaryIO(r, "01SID", "/data") {
		t.Error("malformed tool_input must not be auto-allow eligible")
	}
}
