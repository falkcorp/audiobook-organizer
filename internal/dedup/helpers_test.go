// file: internal/dedup/helpers_test.go
// version: 1.0.1
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-09-02

package dedup

// strPtr returns a pointer to a string value.
//
//go:fix inline
func strPtr(s string) *string {
	return new(s)
}
