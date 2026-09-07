// file: internal/database/sql_dialect.go
// version: 1.0.0
// guid: 7d2f1a90-4c8b-4e23-9f61-2b7c5d0e8a44
// last-edited: 2026-09-07

// Package database — SQL dialect seam for the backend-agnostic activity store.
//
// SQLActivityStore is written once against database/sql and this small
// sqlDialect interface. Everything that differs between engines — placeholder
// style, DDL, and the two non-standard predicates the activity filter needs
// (case-sensitive substring match on summary, and "does this JSON array column
// contain value X") — lives behind the interface, so adding MySQL or Postgres
// is a new dialect + driver import, not a rewrite of the store body.
//
// Only sqliteDialect is implemented today (modernc.org/sqlite, no cgo). MySQL
// and Postgres dialects are additive: implement the four methods, add the
// driver import, and teach the factory in internal/activity to build one from a
// DSN. Wiring a networked DB (credentials, connection limits, migrations) is a
// deliberate operational decision, not part of the SQLite dark-launch.
package database

import (
	"fmt"
	"strings"
)

// sqlDialect abstracts the engine-specific fragments SQLActivityStore needs.
// The store composes portable ANSI SQL around these; it never writes an
// engine-specific string directly.
type sqlDialect interface {
	// name identifies the dialect for logging.
	name() string
	// driverName is the database/sql driver to open (e.g. "sqlite").
	driverName() string
	// ddl returns the ordered schema statements (table + indexes), each safe to
	// run repeatedly (IF NOT EXISTS).
	ddl() []string
	// rebind converts a query written with '?' placeholders into the dialect's
	// placeholder style. Identity for SQLite/MySQL; $1,$2… for Postgres.
	rebind(query string) string
	// substringMatch returns a case-sensitive "col contains ?" predicate, one
	// '?' placeholder bound to the needle. Matches Go strings.Contains, which is
	// what the Pebble/Nuts activity filter uses (NOT case-insensitive LIKE).
	substringMatch(col string) string
	// jsonArrayContains returns a "the JSON-array column col contains value ?"
	// predicate, one '?' placeholder bound to the tag value.
	jsonArrayContains(col string) string
}

// ── SQLite ────────────────────────────────────────────────────────────────────

type sqliteDialect struct{}

func (sqliteDialect) name() string       { return "sqlite" }
func (sqliteDialect) driverName() string { return "sqlite" } // modernc.org/sqlite registers "sqlite"

func (sqliteDialect) ddl() []string {
	// src_key holds the origin backend's primary key (Pebble
	// "act:<tier>:<nano>:<ulid>") for rows copied by the migration backfill. It
	// is UNIQUE so the backfill is idempotent (re-running INSERT OR IGNOREs a
	// row already copied); SQLite treats multiple NULLs as distinct, so native
	// SQLite-origin writes leave it NULL and never collide. It doubles as the
	// cross-reference used to verify backfill parity against Pebble.
	return []string{
		`CREATE TABLE IF NOT EXISTS activity (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			src_key      TEXT,
			ts           INTEGER NOT NULL,
			tier         TEXT    NOT NULL,
			type         TEXT    NOT NULL,
			level        TEXT    NOT NULL,
			source       TEXT    NOT NULL,
			operation_id TEXT,
			book_id      TEXT,
			summary      TEXT    NOT NULL,
			details      TEXT,
			tags         TEXT,
			pruned_at    INTEGER
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_act_srckey ON activity(src_key) WHERE src_key IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_act_tier_ts ON activity(tier, ts)`,
		`CREATE INDEX IF NOT EXISTS idx_act_ts       ON activity(ts)`,
		`CREATE INDEX IF NOT EXISTS idx_act_op_ts    ON activity(operation_id, ts) WHERE operation_id IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_act_bk_ts    ON activity(book_id, ts)      WHERE book_id IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_act_source   ON activity(source)`,
	}
}

// rebind is identity for SQLite: it uses '?' natively.
func (sqliteDialect) rebind(query string) string { return query }

// substringMatch: instr is case-sensitive on TEXT, matching strings.Contains.
func (sqliteDialect) substringMatch(col string) string {
	return fmt.Sprintf("instr(%s, ?) > 0", col)
}

// jsonArrayContains: json_each expands the array; the correlated EXISTS is the
// portable-across-SQLite-versions way to test membership.
func (sqliteDialect) jsonArrayContains(col string) string {
	return fmt.Sprintf("EXISTS (SELECT 1 FROM json_each(%s) WHERE value = ?)", col)
}

// ── helpers shared by every dialect ─────────────────────────────────────────

// placeholders returns "?,?,…" with n marks, for building IN (…) lists before
// rebind. Returns "NULL" for n<=0 so "IN (NULL)" is a valid, always-false-ish
// list rather than a syntax error (callers guard n==0 anyway).
func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
