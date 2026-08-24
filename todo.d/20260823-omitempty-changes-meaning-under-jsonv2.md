- [ ] **JSONV2-OMITEMPTY** `omitempty` means something different in
      `encoding/json` v1 and `encoding/json/v2`, and this repo is part-way
      through migrating between them. **153 struct fields across 70 structs in
      54 files** will change their serialized shape when their package moves.

      Measured 2026-08-23 on this module (go 1.26.0, `GOEXPERIMENT=jsonv2`,
      which the Makefile exports and every CI workflow sets):

      | Go field    | tag         | v1 output   | v2 output          |
      |-------------|-------------|-------------|--------------------|
      | `bool` false | `omitempty` | omitted     | `"bo":false`       |
      | `int` 0      | `omitempty` | omitted     | `"in":0`           |
      | empty struct | `omitempty` | `"s":{}`    | `"s":{"a":false}`  |
      | any zero     | `omitzero`  | omitted     | omitted            |

      v1's `omitempty` means "the Go value is a zero value". v2's means "it
      encodes to an **empty JSON value**" — and `false` and `0` are not empty
      JSON values, only `""`, `null`, `{}` and `[]` are. So the tag silently
      changes meaning for every bool and every number.

      **Census by AST, not by grep** (a naming grep cannot tell `omitempty` on a
      `bool` from `omitempty` on a `*string`): 153 fields whose type is a bare
      bool/int/uint/float. Worst offenders:

          internal/database   42     FileDiagnostic      15
          internal/diagnosis  15     BookFile            12
          internal/metafetch  13     BookFileCore        12
          plugins/maintenance 13     MetadataCandidate   10
          internal/server     12     BookDocument         9

      **Why it matters here specifically.** `internal/database` still imports v1
      and is what persists rows to Pebble. 17 files elsewhere already import
      `encoding/json/v2`. The day `internal/database` migrates, every
      `book_file` row gains ~12 zero-valued numeric keys (`track_number`,
      `track_count`, `disc_number`, `disc_count`, `duration`, `file_size`,
      `bitrate_kbps`, `sample_rate_hz`, `channels`, `bit_depth`,
      `acoustid_fingerprint_duration_sec`, `acoustid_online_score`) that were
      previously absent. Bigger rows, and any consumer distinguishing "absent"
      from "zero" changes its answer — the exact shape of the
      `is_primary_version` nil/absent divergence already tracked in this file.

      **Fix direction:** retag affected fields `omitzero`, which means the same
      thing under both. Mechanical but not blind — a field where "absent" and
      "zero" genuinely differ needs a decision, not a sed. Do it package by
      package, ahead of that package's v2 migration, not in one sweep.

      Found while adding `ScanState` to `BookFile`
      (`internal/database/scan_state.go`), which is tagged `omitzero` throughout
      and carries the measured table in its doc comment.
      `TestBookFile_ScanObjectSerializesIdenticallyAcrossMarshalers` pins the new
      field against both marshalers; it is deliberately scoped to the `scan`
      object because the rest of `BookFile` does not have that property today.
