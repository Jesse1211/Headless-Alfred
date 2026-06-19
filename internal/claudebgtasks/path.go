package claudebgtasks

import (
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/text/unicode/norm"
)

// MangleCwd implements Claude CLI's cwd → path-segment encoding.
// 1. NFC-normalise the input.
// 2. realpath-resolve (caller's responsibility — we receive the
//    resolved cwd; ADR-008 says handleClaudePrompt already does this).
// 3. Replace every non-[a-zA-Z0-9] char with '-'.
// 4. If the result is > 200 chars, truncate to 200 + "-" + base-36
//    Math.abs(javaHash(originalCwd)).
//
// `originalCwd` is the post-NFC, post-realpath path. The hash input
// is `originalCwd` (NOT the truncated string).
func MangleCwd(realpathCwd string) string {
	nfc := norm.NFC.String(realpathCwd)
	mangled := make([]byte, 0, len(nfc))
	for _, r := range nfc {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			mangled = append(mangled, byte(r))
		} else {
			mangled = append(mangled, '-')
		}
	}
	if len(mangled) > 200 {
		h := javaHash(nfc)
		if h < 0 {
			h = -h
		}
		suffix := strconv.FormatInt(int64(h), 36)
		return string(mangled[:200]) + "-" + suffix
	}
	return string(mangled)
}

// javaHash matches Claude CLI's reducer: h = ((h<<5)-h+ch)|0,
// reduced over the string's UTF-16 code units. Because Go strings
// are UTF-8, we iterate runes and re-emit UTF-16 surrogate pairs
// as needed. Returns the 32-bit truncated result as an int32.
func javaHash(s string) int32 {
	var h int32
	for _, r := range s {
		if r <= 0xFFFF {
			h = (h<<5 - h + int32(r))
		} else {
			// Surrogate pair
			rmin := r - 0x10000
			hi := int32(0xD800 + (rmin >> 10))
			lo := int32(0xDC00 + (rmin & 0x3FF))
			h = (h<<5 - h + hi)
			h = (h<<5 - h + lo)
		}
	}
	return h
}

// OutputPath returns the file path where Claude CLI writes a given
// bg task's stdout. Reads ALFRED_CLAUDE_BG_TASK_DIR; if unset,
// falls back to "/tmp/claude-<uid>" (matching the CLI's own
// default).
func OutputPath(realpathCwd, sessionUUID, taskID string) string {
	base := os.Getenv("ALFRED_CLAUDE_BG_TASK_DIR")
	if base == "" {
		base = filepath.Join("/tmp", "claude-"+strconv.Itoa(os.Getuid()))
	}
	return filepath.Join(base, MangleCwd(realpathCwd), sessionUUID, "tasks", taskID+".output")
}
