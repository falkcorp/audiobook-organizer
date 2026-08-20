// file: cmd/pebble-inject-skip/main.go
// version: 1.0.2
// guid: a1b2c3d4-e5f6-7a8b-9c0d-e1f2a3b4c5d6
// last-edited: 2026-08-20
//
// One-shot tool: writes transcode skip flags directly into PebbleDB.
// Run while the service is stopped.

package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/cockroachdb/pebble/v2"
)

type Setting struct {
	Key      string
	Value    string
	Type     string
	IsSecret bool
}

func main() {
	if len(os.Args) < 2 {
		slog.Error("usage pebble-inject-skip <pebble-db-path>")
		os.Exit(1)
	}
	dbPath := os.Args[1]

	db, err := pebble.Open(dbPath, &pebble.Options{})
	if err != nil {
		slog.Error("open pebble failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	keys := []string{
		"transcode_skip_5a89c8d2857e4899", // David Petrie
		"transcode_skip_2b2d16b94596fe1a", // Eric Ugland "One More Last Time"
	}

	for _, k := range keys {
		s := Setting{Key: k, Value: "true", Type: "bool", IsSecret: false}
		val, err := json.Marshal(s)
		if err != nil {
			slog.Error("marshal failed", "key", k, "error", err)
			os.Exit(1)
		}
		dbKey := "setting:" + k
		if err := db.Set([]byte(dbKey), val, pebble.Sync); err != nil {
			slog.Error("set failed", "key", dbKey, "error", err)
			os.Exit(1)
		}
		fmt.Printf("SET %s = %s\n", dbKey, val)
	}

	fmt.Println("Done.")
}
