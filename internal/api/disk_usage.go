package api

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func defaultUserHomeDir() (string, error) { return os.UserHomeDir() }

// DiskUsage reports how much of the configured PVC quota Alfred has
// actually consumed. We deliberately do NOT use statfs here: in our
// deploy (k3s local-path provisioner) the underlying filesystem is
// the oracle node's whole disk, so statfs would report 100GB total
// and 19% used — which would confuse the user ("alfred says 19%
// used but the PVC was 5Gi!") and would never trigger the alert
// banner because we'd never push the host disk to 80%.
//
// Instead we walk the two Alfred-owned directories and sum file
// sizes (effectively a `du -sb`), and compare against the PVC
// quota that Helm passed via ALFRED_PVC_LIMIT. This is the number
// the user cares about because it's also the number that controls
// when writes start failing.
type DiskUsage struct {
	// Path is just a label for the UI; we walk /data + /home/alfred.
	Path string `json:"path"`
	// Total = the configured PVC quota (ALFRED_PVC_LIMIT). 0 when
	// the env var is missing — frontend should show "unknown" then,
	// not a misleading percentage.
	TotalBytes uint64 `json:"totalBytes"`
	// Used = sum of file sizes under the Alfred-owned trees.
	UsedBytes uint64 `json:"usedBytes"`
	// Available = Total - Used (clamped to 0). Frontend uses this
	// and TotalBytes to compute its own display rather than trusting
	// our percentage.
	AvailableBytes uint64 `json:"availableBytes"`
	// Percent = 100 * Used / Total. 0 when Total is 0.
	UsedPercent float64 `json:"usedPercent"`
}

// readDiskUsage walks the Alfred directories and returns a usage
// snapshot keyed against the configured quota. dataDir is the
// alfred-server's ALFRED_DATA_DIR (/data in pod, /tmp/... in dev).
// We always also walk the user's home (~/.claude on PVC) since
// jsonl transcripts are the biggest growth vector and they live
// there.
func readDiskUsage(dataDir string, quotaBytes uint64) (DiskUsage, error) {
	usedData, err := dirSize(dataDir)
	if err != nil {
		return DiskUsage{}, fmt.Errorf("walk dataDir: %w", err)
	}
	usedHome, err := dirSize(userHomeDir())
	if err != nil {
		// home walk failures are non-fatal — we'd rather report
		// a slight under-count than fail the whole endpoint.
		usedHome = 0
	}
	used := usedData + usedHome
	avail := uint64(0)
	if quotaBytes > used {
		avail = quotaBytes - used
	}
	var percent float64
	if quotaBytes > 0 {
		percent = float64(used) / float64(quotaBytes) * 100
		percent = float64(int64(percent*10+0.5)) / 10
	}
	return DiskUsage{
		Path:           dataDir,
		TotalBytes:     quotaBytes,
		UsedBytes:      used,
		AvailableBytes: avail,
		UsedPercent:    percent,
	}, nil
}

// dirSize returns the sum of file sizes under root. Symlinks are
// not followed (avoid double-counting via /home/alfred → /data
// shenanigans if someone ever sets them up). Permission errors
// inside the tree are logged at the call site and ignored — partial
// numbers are better than no number.
func dirSize(root string) (uint64, error) {
	var total uint64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			// Don't propagate per-file errors; skip + continue.
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		// Skip symlinks; we follow with Info on the link itself, not
		// the target, but treat it as 0 bytes to avoid counting the
		// target twice if it's also in the walked tree.
		if info.Mode()&fs.ModeSymlink != 0 {
			return nil
		}
		total += uint64(info.Size())
		return nil
	})
	return total, err
}

// userHomeDir resolves ~ for the running process. In the pod that's
// /home/alfred (= the PVC subPath). In dev that's whatever $HOME is.
// On error we return /home/alfred as a reasonable default — the
// walk will just no-op if the dir doesn't exist.
func userHomeDir() string {
	if h, err := osUserHomeDir(); err == nil && h != "" {
		return h
	}
	return "/home/alfred"
}

// Pulled into a named indirection so the test can swap it. Default
// uses os.UserHomeDir.
var osUserHomeDir = defaultUserHomeDir

// ParsePVCLimit converts a Helm-style size string (5Gi, 500Mi, 2G,
// 100M, "1024") into bytes. Recognises both binary (Ki/Mi/Gi/Ti)
// and decimal (K/M/G/T) prefixes. Returns 0 on parse failure — the
// caller surfaces this as "unknown quota" in the UI.
func ParsePVCLimit(s string) uint64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// Find the boundary between digits and suffix.
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	numStr := s[:i]
	unit := strings.TrimSpace(s[i:])
	n, err := strconv.ParseFloat(numStr, 64)
	if err != nil || n < 0 {
		return 0
	}
	var mult uint64
	switch unit {
	case "", "B":
		mult = 1
	case "K":
		mult = 1000
	case "Ki":
		mult = 1024
	case "M":
		mult = 1000 * 1000
	case "Mi":
		mult = 1024 * 1024
	case "G":
		mult = 1000 * 1000 * 1000
	case "Gi":
		mult = 1024 * 1024 * 1024
	case "T":
		mult = 1000 * 1000 * 1000 * 1000
	case "Ti":
		mult = 1024 * 1024 * 1024 * 1024
	default:
		return 0
	}
	return uint64(n * float64(mult))
}

// DiskUsageHandler returns GET /api/disk-usage. Read-only, used by
// both the disk-pressure banner (over the WS push frame) and by
// debug curls. quotaBytes is captured at handler-build time from
// the ALFRED_PVC_LIMIT env (parsed in main.go).
func DiskUsageHandler(dataDir string, quotaBytes uint64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		du, err := readDiskUsage(dataDir, quotaBytes)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "scan_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, du)
	})
}
