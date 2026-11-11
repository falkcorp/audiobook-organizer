// file: core/config/doc.go
// version: 1.1.2
// guid: e74dba99-865d-4688-bd81-8e7ca26610c8
// last-edited: 2026-11-10

// Package config is core's configuration engine (05-config §1.3). A module or
// the kernel declares a section as a typed value (Section[T], Define and its
// options, the Path grammar, the Reload and Protection vocabularies,
// Duration, Validator, FieldError) and registers it on a Store (Register),
// which merges defaults, the operator layers (file, env, flags), the boot
// record and the stored overrides into immutable snapshots read through
// Handle[T].Get, Pending, OnChange and AfterCommit.
//
// The Store boots without storage (Boot reads the boot record under
// core.paths.state_dir, resolved by ResolveStateDir, the one pre-boot rule),
// seals at Load, reads the DB layer once at Attach (writing nothing), and
// changes values only through Apply, which authorizes, validates, writes the
// boot record and hands one Commit to the kernel's Committer. A key an
// operator layer supplies is locked; ProtectOperator keys are never stored.
// KeyState, Keys and Schema describe every key for the settings UI; secret
// values never appear in them. Importer stages a cutover step's rows.
//
// The env and file grammar, FailClosed and retired names are F3-02's; the
// secrets store and its stager are F3-03's; the kernel wires the Store at
// F3-07. Until then the operator layers that reach a Store are the
// explicitly set flags, and a configured operator file is refused.
//
// # Field metadata
//
// Metadata about a key is a struct tag on the section's type T. The tag
// vocabulary is closed:
//
//	json:"name"                    the key segment; decoding is encoding/json
//	reload:"restart|boot"          ReloadRestart or ReloadBoot; absent means ReloadLive
//	protect:"interactive|admin|owner|operator"
//	                               the Protection; absent means ProtectNone
//	schema:"minimum=1,enum=a|b"    JSON Schema keywords: minimum=, maximum=,
//	                               enum=a|b, format=duration|uri|cidr|path|email,
//	                               pattern=
//	sensitive:"true"               not a secret, but redacted in diagnostics
//	                               bundles and logs
//
// A tag on a struct-typed field applies to every key beneath it. A tag
// inside a key's value (a list, map, array or pointer element, or a struct
// that encodes itself) may not be stricter than the key: config protects,
// reloads and redacts whole keys, so Register refuses it with ErrInvalid.
//
// # Secrets
//
// A field of type secrets.Ref is a secret key whose value lives in the
// secrets store; the snapshot holds only its bound reference. A secrets.Ref
// may also be a direct field of a list entry whose type has a string field
// json "id" ("entry secrets", keys.go): each entry's Ref is then a secret key
// of its own, "<list parent>.<id>.<field>" (05-authn's
// core.auth.oidc.<id>.client_secret), carrying its own protect tag, which
// Register requires. Removing or renaming an entry through Apply deletes its
// secret in the same commit.
package config
