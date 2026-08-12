# iTunes / Apple Devices Sync Diagnostic Suite

## What this is

A battery of `iTunes Library.itl` test variants generated from a known-good
**baseline** (your pre-audiobook-organizer library), each with a single
isolated change. Goal: figure out why audiobook-organizer-written ITLs open
fine in iTunes 12.13 but break **Apple Devices** at step 3 ("Determining
tracks to sync") with *"An error has occurred. You will need to restart Apple
Devices."*

Apple Devices has no programmatic API, so finding the breaking axis requires
manual sync-attempts against many small variations.

## Tools

| Tool | Purpose |
|---|---|
| `cmd/itunes-sync-tests` | Generate the suite from a baseline ITL |
| `cmd/verify-suite` | Sanity-check every variant parses + has expected track count |
| `cmd/itl-diff` | Structural diff between two ITLs (header, msdh, tracks, mhoh) |
| `scripts/sync-tests-windows.ps1` | Windows runner that swaps each variant in, probes iTunes via COM, prompts for the manual Apple Devices verdict, and writes `result.json` |

## Generating the suite

```bash
go run ./cmd/itunes-sync-tests \
    -baseline "/path/to/your/iTunes Library.itl" \
    -out      "/path/to/sync-tests"
```

Then:

```bash
go run ./cmd/verify-suite -suite "/path/to/sync-tests"
```

Copy the `sync-tests/` folder + `scripts/sync-tests-windows.ps1` to Windows.

## Running on Windows

> **Before running, manually back up your iPhone AND your
> `%USERPROFILE%\Music\iTunes\` folder.** The script will refuse to start
> until you pass `-ConfirmBackup`.

```powershell
cd C:\path\to\sync-tests
.\sync-tests-windows.ps1 -SuiteRoot "C:\path\to\sync-tests" -ConfirmBackup
```

For each test the script:
1. Closes iTunes / Apple Devices.
2. Copies the variant's `iTunes Library.itl` into your real iTunes folder.
3. Launches iTunes via COM and records `LibraryPlaylist.Tracks.Count`.
4. Launches **Apple Devices** and prompts you to attempt a sync.
5. Pops a Yes/No/Cancel dialog → writes `result.json`.

After the loop, the script restores your original `iTunes Library.itl` from
its one-time auto-backup at `%USERPROFILE%\sync-tests-backup\`.

Use `-Resume` to skip tests that already have a non-`did-not-test` result.

## Findings already surfaced by the diff tool

Running `itl-diff` between the baseline and our generated variants
**before** trying anything on Windows already revealed:

1. **Round-trip is byte-faithful at the parser level.** Variant 01
   (`01-roundtrip-only`) decrypts → inflates → deflates → encrypts the
   baseline with no payload edits, and the diff tool reports `Tracks
   only in A: 0` / `Tracks changed: 0`. The file is ~350KB larger purely
   because Go's deflate emits at a different compression level than
   iTunes does. *This rules out our pipeline as the cause* of the sync
   failure — the bytes round-trip cleanly.

2. **Synthetic tracks lose their Name/Artist when re-parsed.** Variants
   02-12 and 38 add a single new track via `AddTracksLE`. The diff tool
   re-parses the result and reports the new track with empty
   `Name`/`Artist`. Two possibilities:
   - Our LE parser walks `mhoh` chunks only inside `miah` containers,
     and `AddTracksLE` writes raw `mith`+`mhoh` without wrapping —
     parser-side asymmetry only.
   - Real iTunes is lenient and reads them anyway (consistent with the
     user observation that books appear correctly in iTunes), but
     **Apple Devices may use a stricter parser** that follows the same
     rules our parser does. **This is the prime suspect.**

   The right test is variant 12 (`12-add-1-track-fullhouse`) — if it
   *also* fails Apple Devices, the issue is the missing `miah` wrapper.

3. **The hdfm header `fileLen` field at offset 0x08 is the only header
   delta** between baseline and any variant. That field is expected to
   change with payload size; it is not a checksum.

## Variant index

See `index.json` for the machine-readable list, or each test folder's
`test-info.json` for the hypothesis it targets.

After running on Windows, summarize results with:

```bash
jq -s '[.[] | {id: .id, opens: .opens_in_itunes, sync: .apple_devices_sync, notes: .notes}]' \
   sync-tests/*/result.json
```
