// file: core/config/errors.go
// version: 1.2.0
// guid: 00f06753-8c6a-48c9-adf5-5229340bfd87
// last-edited: 2026-11-10

package config

import (
	"errors"
	"fmt"
	"strings"
)

// Validator is implemented by section types that need cross-field checks. It
// runs on every candidate snapshot before commit; return errors.Join of field
// errors wrapped with FieldError so the UI can attach them to keys. A
// Validator never formats a sensitive field's value into its error (05-config
// §1.3.1). Config does not trust it to: in a section that holds a sensitive
// key, no message a Validator writes is kept (fail closed). A FieldError on
// one of the section's keys, sensitive or plain, keeps its path and gets a
// fixed message in place of its own (in a map section, only a key of an entry
// the checked candidate holds: the path is Validator text too), and any other
// part of the error is replaced by a fixed message on the section. A section
// with no sensitive key keeps its Validator's messages as written.
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

// Config's sentinel errors (05-config §1.3.3).
var (
	ErrUnknownKey  = errors.New("config: unknown key")
	ErrLocked      = errors.New("config: key is set by the operator")      // wrapped in *LockedError
	ErrProtected   = errors.New("config: key needs a stronger credential") // wrapped in *ProtectedError
	ErrForbidden   = errors.New("config: missing section permission")
	ErrInvalid     = errors.New("config: invalid value") // errors.Join of *FieldError
	ErrSealed      = errors.New("config: registry sealed after Load")
	ErrDuplicate   = errors.New("config: duplicate path or env name")
	ErrRetiredName = errors.New("config: a retired name is set") // wrapped in *RetiredNameError
)

// LockedError is Apply's refusal of a key an operator layer supplies
// (05-config §1.3.5). By names the layer: "env ABO_CORE_LOG_LEVEL",
// "flag core.storage.path", "file <name>".
type LockedError struct {
	Path Path
	By   string
}

// Error names the key and the operator source.
func (e *LockedError) Error() string {
	return fmt.Sprintf("config: %s is set by the operator (%s)", e.Path, e.By)
}

// Unwrap returns ErrLocked.
func (e *LockedError) Unwrap() error { return ErrLocked }

// ProtectedError is Apply's refusal of a key whose Protection the actor does
// not meet (05-config §1.3.7). Need is the level the key requires.
type ProtectedError struct {
	Path Path
	Need Protection
}

// Error names the key and the protection it needs.
func (e *ProtectedError) Error() string {
	return fmt.Sprintf("config: %s needs %s", e.Path, e.Need)
}

// Unwrap returns ErrProtected.
func (e *ProtectedError) Unwrap() error { return ErrProtected }

// PostCommitError is an AfterCommit hook's error: the change stays committed
// (05-config §1.3.2).
type PostCommitError struct {
	Section Path
	Err     error
}

// Error names the section whose hook failed.
func (e *PostCommitError) Error() string {
	return fmt.Sprintf("config: %s: committed, but an after-commit hook failed: %v", e.Section, e.Err)
}

// Unwrap returns the hook's error.
func (e *PostCommitError) Unwrap() error { return e.Err }

// RetiredNameError lists every refused use of a retired name, so one boot
// names them all (05-config §1.3.6). Boot and Load return it before storage
// opens (retired.go).
type RetiredNameError struct{ Uses []RetiredUse }

// Error lists each refused name with its canonical env name and its key in
// the operator file.
func (e *RetiredNameError) Error() string {
	parts := make([]string, 0, len(e.Uses))
	for _, u := range e.Uses {
		kind := "env"
		if u.File {
			kind = "file key"
		}
		parts = append(parts, fmt.Sprintf("%s %s (now env %s or file key %s)", kind, u.Name, u.CanonicalEnv, u.Canonical))
	}
	return "config: retired names of protected or secret keys are set; they are never read, so the service stops rather than run without them: " + strings.Join(parts, "; ")
}

// Unwrap returns ErrRetiredName.
func (e *RetiredNameError) Unwrap() error { return ErrRetiredName }

// String names a Protection level as its struct tag spells it.
func (p Protection) String() string {
	switch p {
	case ProtectNone:
		return "none"
	case ProtectInteractive:
		return "interactive"
	case ProtectAdmin:
		return "admin"
	case ProtectOwner:
		return "owner"
	case ProtectOperator:
		return "operator"
	}
	return fmt.Sprintf("Protection(%d)", uint8(p))
}

// String names a Reload class as the schema's x-mediacore-reload spells it.
func (r Reload) String() string {
	switch r {
	case ReloadLive:
		return "live"
	case ReloadRestart:
		return "restart"
	case ReloadBoot:
		return "boot"
	}
	return fmt.Sprintf("Reload(%d)", uint8(r))
}

// invalid joins field errors under ErrInvalid, so errors.Is(err, ErrInvalid)
// holds and errors.As finds each *FieldError.
func invalid(errs ...error) error {
	return fmt.Errorf("%w: %w", ErrInvalid, errors.Join(errs...))
}
