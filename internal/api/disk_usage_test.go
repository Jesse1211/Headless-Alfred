package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParsePVCLimit(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
	}{
		{"", 0},
		{"  ", 0},
		{"5Gi", 5 * 1024 * 1024 * 1024},
		{"500Mi", 500 * 1024 * 1024},
		{"2G", 2 * 1000 * 1000 * 1000},
		{"100M", 100 * 1000 * 1000},
		{"1024", 1024},
		{"1024B", 1024},
		{"1Ki", 1024},
		{"0.5Gi", 512 * 1024 * 1024},
		{"garbage", 0},
		{"5Xi", 0},          // unknown suffix
		{"-5Gi", 0},         // negative
		{"5 Gi", 5 * 1024 * 1024 * 1024}, // unit may have a space
	}
	for _, c := range cases {
		if got := ParsePVCLimit(c.in); got != c.want {
			t.Errorf("ParsePVCLimit(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestReadDiskUsage_QuotaPercent(t *testing.T) {
	// Build a tiny tree under /tmp and probe it.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.bin"), make([]byte, 600), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.bin"), make([]byte, 400), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force userHomeDir to a separate empty dir so it doesn't add bytes.
	emptyHome := t.TempDir()
	old := osUserHomeDir
	osUserHomeDir = func() (string, error) { return emptyHome, nil }
	defer func() { osUserHomeDir = old }()

	du, err := readDiskUsage(root, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if du.UsedBytes != 1000 {
		t.Errorf("UsedBytes = %d, want 1000", du.UsedBytes)
	}
	if du.TotalBytes != 2000 {
		t.Errorf("TotalBytes = %d, want 2000", du.TotalBytes)
	}
	if du.AvailableBytes != 1000 {
		t.Errorf("AvailableBytes = %d, want 1000", du.AvailableBytes)
	}
	if du.UsedPercent != 50.0 {
		t.Errorf("UsedPercent = %v, want 50.0", du.UsedPercent)
	}
}

func TestReadDiskUsage_NoQuotaMeansZeroPercent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "x"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	emptyHome := t.TempDir()
	old := osUserHomeDir
	osUserHomeDir = func() (string, error) { return emptyHome, nil }
	defer func() { osUserHomeDir = old }()

	du, err := readDiskUsage(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if du.TotalBytes != 0 || du.UsedPercent != 0 {
		t.Errorf("expected zeros, got %+v", du)
	}
	if du.UsedBytes != 2 {
		t.Errorf("UsedBytes = %d, want 2", du.UsedBytes)
	}
}

func TestReadDiskUsage_HomeWalkFailureIsNonFatal(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "x"), []byte("yo"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := osUserHomeDir
	osUserHomeDir = func() (string, error) { return "/nonexistent-zzz-9999", nil }
	defer func() { osUserHomeDir = old }()
	du, err := readDiskUsage(root, 100)
	if err != nil {
		t.Fatalf("home walk failure must not propagate: %v", err)
	}
	if du.UsedBytes != 2 {
		t.Errorf("UsedBytes = %d, want 2 (home should not contribute)", du.UsedBytes)
	}
}
