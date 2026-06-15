// Package recap is the on-disk recap-file location helper.
// Files live at <dataDir>/recaps/<YYYY-MM-DD>.md.
package recap

import "path/filepath"

// Dir returns the directory holding all recap files.
func Dir(dataDir string) string {
	return filepath.Join(dataDir, "recaps")
}

// Path returns the absolute path of the recap file for the given
// local date string (YYYY-MM-DD). No validation — caller is responsible
// for shape.
func Path(dataDir, date string) string {
	return filepath.Join(Dir(dataDir), date+".md")
}
