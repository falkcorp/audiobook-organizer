// file: internal/books/appconfig/doc.go
// version: 1.0.0
// guid: 8d3b6f1a-2c47-4e90-a5d8-7b1e0c4f9a26
// last-edited: 2026-11-19

// Package appconfig is ABO's config Facade (05-config section 1.12.1 item 2),
// the implementation of internal/config.Facade over the App's core config
// store.
//
// HEAD's Config is one flat struct that 215 files read through
// config.AppConfig. From the flip its values live in core config keys and the
// secret store, under the names the rename table (internal/books/cutover)
// gives them. The Engine projects them back into the flat Config, through the
// table and its inverse converters, on every core generation change, and
// publishes the result through config.Project. A write goes the other way:
// Mutate diffs the Config it is given against the projection and turns the
// difference into one core Apply (ordinary keys), plus secret-store writes for
// the integrations secrets that no config section registers.
//
// The package reads the table, not names: a row added to the table is
// projected without a line here. It imports core config only through
// module.ConfigAccess, the handle the kernel gives the one module that
// declares Manifest.AppConfig.
package appconfig
