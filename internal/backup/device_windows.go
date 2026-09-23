// file: internal/backup/device_windows.go
// version: 1.0.0
// guid: f8fde119-7c26-41d6-8d28-08ab7bcec1d7
// last-edited: 2026-09-22

//go:build windows

package backup

// deviceID has no cheap st_dev equivalent on Windows. Reporting "unknown"
// skips the same-device guard rather than refusing every backup there.
func deviceID(path string) (dev uint64, ok bool, err error) {
	return 0, false, nil
}
