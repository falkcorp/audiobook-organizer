// file: core/config/errors.go
// version: 1.0.0
// guid: 00f06753-8c6a-48c9-adf5-5229340bfd87
// last-edited: 2026-11-10

package config

// Validator is implemented by section types that need cross-field checks. It
// runs on every candidate snapshot before commit; return errors.Join of field
// errors wrapped with FieldError so the UI can attach them to keys.
type Validator interface{ Validate() error }

// FieldError attaches a validation failure to the key it concerns.
type FieldError struct {
	Path Path
	Err  error
}

// Error reports the path and the underlying error.
func (e *FieldError) Error() string {
	if e.Err == nil {
		return string(e.Path)
	}
	return string(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *FieldError) Unwrap() error { return e.Err }
