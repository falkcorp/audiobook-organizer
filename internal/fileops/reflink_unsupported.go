// file: internal/fileops/reflink_unsupported.go
// version: 1.0.0
// guid: c359398e-55cf-4e90-b07e-335496a37ab9
// last-edited: 2026-09-01

//go:build !linux && !darwin

package fileops

// reflinkPlatform reports that this platform has no copy-on-write clone
// primitive, so ReflinkOrCopy falls back to a byte copy. Windows ReFS has
// block cloning but exposes it only through DeviceIoControl on a volume
// configured for it, which nothing here targets.
func reflinkPlatform(src, dst string) error {
	return ErrReflinkUnsupported
}
