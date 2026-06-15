package claude

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func mkRecapReq(t *testing.T, tool, path, content string) PendingRequest {
	t.Helper()
	type writeIn struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	type readIn struct {
		FilePath string `json:"file_path"`
	}
	var raw []byte
	var err error
	if tool == "Write" {
		raw, err = json.Marshal(writeIn{FilePath: path, Content: content})
	} else {
		raw, err = json.Marshal(readIn{FilePath: path})
	}
	if err != nil {
		t.Fatal(err)
	}
	return PendingRequest{ToolName: tool, ToolInput: raw}
}

func TestIsRecapIO_AllowsRead(t *testing.T) {
	dataDir := "/data"
	path := filepath.Join(dataDir, "recaps", "2026-06-15.md")
	req := mkRecapReq(t, "Read", path, "")
	if !isRecapIO(req, dataDir) {
		t.Errorf("Read of valid recap path should auto-allow")
	}
}

func TestIsRecapIO_AllowsWriteUnderCap(t *testing.T) {
	dataDir := "/data"
	path := filepath.Join(dataDir, "recaps", "2026-06-15.md")
	req := mkRecapReq(t, "Write", path, strings.Repeat("x", 8000))
	if !isRecapIO(req, dataDir) {
		t.Errorf("Write under cap should auto-allow")
	}
}

func TestIsRecapIO_DeniesOversizedWrite(t *testing.T) {
	dataDir := "/data"
	path := filepath.Join(dataDir, "recaps", "2026-06-15.md")
	req := mkRecapReq(t, "Write", path, strings.Repeat("x", maxRecapWriteBytes+1))
	if isRecapIO(req, dataDir) {
		t.Errorf("oversized Write must fall through to user")
	}
}

func TestIsRecapIO_DeniesBadFilename(t *testing.T) {
	dataDir := "/data"
	for _, name := range []string{
		"2026-6-15.md",
		"hello.md",
		"2026-06-15.txt",
		"2026-06-15.md.bak",
	} {
		req := mkRecapReq(t, "Read", filepath.Join(dataDir, "recaps", name), "")
		if isRecapIO(req, dataDir) {
			t.Errorf("name %q should NOT auto-allow", name)
		}
	}
}

func TestIsRecapIO_DeniesPathTraversal(t *testing.T) {
	dataDir := "/data"
	req := mkRecapReq(t, "Read", "/data/recaps/../../../etc/passwd", "")
	if isRecapIO(req, dataDir) {
		t.Errorf("traversal should NOT auto-allow")
	}
}

func TestIsRecapIO_DeniesOtherTools(t *testing.T) {
	dataDir := "/data"
	path := filepath.Join(dataDir, "recaps", "2026-06-15.md")
	for _, tool := range []string{"Edit", "Bash", "Glob", "Grep"} {
		req := mkRecapReq(t, tool, path, "")
		if isRecapIO(req, dataDir) {
			t.Errorf("tool %q should NOT auto-allow", tool)
		}
	}
}
