// file: internal/dedup/helpers_test.go
// version: 1.0.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890

package dedup

// strPtr returns a pointer to a string value.
//
//go:fix inline
func strPtr(s string) *string {
	return new(s)
}
