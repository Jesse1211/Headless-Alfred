package api

import (
	"net/http"
	"syscall"
)

// DiskUsage reports the bytes occupied / available on the
// filesystem backing the data dir. /data and /home/alfred share
// one PVC, so reading either path's statfs gives the umbrella
// number — we just probe /data because it always exists. Returned
// values are decimal MiB/GiB through the frontend formatter; this
// struct stays in raw bytes for honest math.
type DiskUsage struct {
	// Path probed (the mountpoint of the PVC inside the pod).
	Path string `json:"path"`
	// Total/Used/Available in bytes. Together they satisfy
	// Used + Available <= Total (reserved blocks for root sit in
	// the gap). Frontend percent should be 100 * Used / (Used+Available)
	// to match `df -h` semantics rather than Used/Total.
	TotalBytes     uint64 `json:"totalBytes"`
	UsedBytes      uint64 `json:"usedBytes"`
	AvailableBytes uint64 `json:"availableBytes"`
	// Percent used, computed as 100 * Used / (Used+Available) so it
	// agrees with `df` and with the "until full" semantic users want.
	// Range 0–100; rounded to one decimal.
	UsedPercent float64 `json:"usedPercent"`
}

// readDiskUsage probes the filesystem holding path via statfs.
// Returns zero values on syscall failure (caller surfaces as 500).
func readDiskUsage(path string) (DiskUsage, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return DiskUsage{Path: path}, err
	}
	total := s.Blocks * uint64(s.Bsize)
	avail := s.Bavail * uint64(s.Bsize)
	free := s.Bfree * uint64(s.Bsize)
	used := total - free
	denom := used + avail
	var percent float64
	if denom > 0 {
		percent = float64(used) / float64(denom) * 100
		// Round to one decimal place.
		percent = float64(int64(percent*10+0.5)) / 10
	}
	return DiskUsage{
		Path:           path,
		TotalBytes:     total,
		UsedBytes:      used,
		AvailableBytes: avail,
		UsedPercent:    percent,
	}, nil
}

// DiskUsageHandler returns GET /api/disk-usage. Read-only, no
// per-session scoping. Used by the frontend's disk-pressure banner
// and by anyone debugging "why is Alfred failing to write".
func DiskUsageHandler(dataDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		du, err := readDiskUsage(dataDir)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "statfs_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, du)
	})
}
