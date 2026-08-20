// file: cmd/itl-audit-encoding/main.go
// version: 1.0.1
// guid: 9d7cc6c7-ae41-4594-b773-93ab73c0a191
// last-edited: 2026-08-20

// itl-audit-encoding walks every mhoh string block in an iTunes-authored .itl
// file and emits a per-hohmType histogram of the byte fields that control
// string encoding (+24 u32, +27 byte, headerLen, totalLen-40-strDataLen delta,
// bytes 32–39 all-zero check). The resulting JSON report is the empirical
// ground truth that mhoh_encoding_table.go is derived from.
//
// Usage:
//
//	go run ./cmd/itl-audit-encoding <path/to/iTunes Library.itl> [output.json]
//
// If the output path is omitted the report is written to stdout.
// The tool walks msdh container types 1 (tracks), 2 (playlists), 9 (albums),
// and 11 (artists) — all container types that carry mhoh string metadata in
// an iTunes-authored library.
//
// The report JSON has the shape:
//
//	{
//	  "library_path": "...",
//	  "library_version": "...",
//	  "audit_date": "2026-06-09",
//	  "total_mhoh_blocks": 12345,
//	  "per_type": {
//	    "2": {
//	      "hohm_type_hex": "0x02",
//	      "count": 94575,
//	      "header_len_values": {"24": 94575},
//	      "at24_values": {"1": 91000, "3": 3575},
//	      "at27_values": {"0": 94575},
//	      "tail_zero": {"true": 94575},
//	      "len_arithmetic_ok": {"true": 94575}
//	    }
//	  }
//	}
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: itl-audit-encoding <library.itl> [output.json]")
		os.Exit(1)
	}

	libPath := os.Args[1]
	outPath := ""
	if len(os.Args) >= 3 {
		outPath = os.Args[2]
	}

	data, err := os.ReadFile(libPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read error: %v\n", err)
		os.Exit(1)
	}

	report, err := itunes.AuditMhohEncoding(data, libPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit error: %v\n", err)
		os.Exit(1)
	}

	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal error: %v\n", err)
		os.Exit(1)
	}

	if outPath == "" {
		fmt.Println(string(out))
		return
	}

	if err := os.WriteFile(outPath, out, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Report written to %s\n", outPath)
}
