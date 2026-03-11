package web

import "os"

// createFile opens a new file for writing, creating parent dirs if needed.
func createFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
}
