// file: core/config/doc.go
// version: 1.0.0
// guid: e74dba99-865d-4688-bd81-8e7ca26610c8
// last-edited: 2026-11-10

// Package config is the declaration surface of core's configuration: the
// Path grammar, the Reload and Protection vocabularies, Duration, the
// Validator and FieldError types, and Section[T] with Define and its options
// (05-config §1.3.1). A module or the kernel declares a section here, as a
// typed value; nothing in this package reads a file, an environment variable
// or a store.
//
// The loader, the Store and Register, key state and locks, the stored-override layer,
// the importer, the JSON Schema reflection (Schema, SchemaFor, Overrides) and
// the configtest package arrive with F3-01 (04 §8.1 F2: "config holds only
// the section declarations until F3"). Until then a declared section changes
// no behaviour: nothing reads its keys before its owner adopts them
// (05-config §1.12.1). The package imports only the standard library.
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
// A tag on a struct-typed field applies to every key beneath it.
package config
