<!-- file: docs/agent-tasks/platform-core/PC-02-05-staging-restore-and-scrub-tools.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6dc5cd9c-0e9b-46ed-8cdf-c8d25dcc243d -->
<!-- last-edited: 2026-11-04 -->

# PC-02-05 — Staging tools: `staging-restore` and `staging-scrub` with the staging marker, `provision.sh`, `secret_fields.txt`, the `legacy-abo/0` manifest sidecar and `boot_budget.py`

| Field | Value |
|---|---|
| ID | PC-02-05 |
| Tier | **T3 Opus/high.** `staging-scrub` destroys credentials, and its only guard is the marker interlock: a store that `staging-restore` did not create, by the same binary, must be refused with nothing written. |
| Wave | W02 (S0, release `S0`) |
| Depends on | PC-02-01 (guard-aware open, `StagingMarker`, `c/storage/install`, `SetBinaryID`) |
| Trailer | `Stage-Release: S0 1` |
| Digest | `docs/plans/platform-core/digests/S0.md` §1 item 7, §4.4 last row, §5 rule 9, §6.9 |
| Spec | 09 §9.3 "Staging" steps 1–3 and the `test_staging_scrub.py` S0 cases; 09 §9.3 "Boot-time budget"; 09 S0 rows "Deploy" and "Data/keys"; 05-storage §5S.3.7 row "Staging marker" and the `StagingMarker` type; 05-backup B7 (the `legacy-abo/0` manifest sidecar) and the `TestStagingRestoreRefusesOtherBinary` row of §5B.9; 05-config §2.2 (the secret-field table) |

**Destructive path.** Run both subcommands only on fixture stores the tests create. Never point them at a store an
agent did not create (plan §(f) risk 9).

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-02-05" -b platform-core/pc-02-05-staging-tools origin/main
cd "$REPO/../audiobook-organizer-pc-02-05"
git rebase origin/main
git worktree list
make web-deps
```

Precondition: `grep -n 'type StagingMarker struct' internal/database/storage_guard.go` prints 1 line (PC-02-01).

## Exact files

Create: `cmd/staging_restore.go`, `cmd/staging_scrub.go`, `cmd/staging_test.go`, `scripts/staging/provision.sh`,
`scripts/staging/secret_fields.txt`, `scripts/tests/test_staging_scrub.py`, `scripts/boot_budget.py`,
`internal/backup/manifest_sidecar.go`, `internal/backup/manifest_sidecar_test.go`.
Modify: `internal/backup/backup.go`, `cmd/root.go`.

## Re-verify greps (expected counts at the planning checkout `51a2c2acd`)

```bash
grep -n 'return backupPath + ".sha256"' internal/backup/backup.go               # 1 hit (line 644)
grep -n 'func DeleteBackupIn' internal/backup/backup.go                          # 1 hit (line 983)
grep -n 'backup failed to delete checksum sidecar' internal/backup/backup.go     # 1 hit (DeleteBackupIn)
grep -n 'backup retention failed to delete checksum sidecar' internal/backup/backup.go  # 1 hit (retention)
grep -n 'failed to persist checksum sidecar' internal/backup/backup.go           # 1 hit (CreateBackup)
grep -n 'func CreateBackupWithCheckpoint' internal/backup/backup.go              # 1 hit (line 428)
grep -n 'func extractArchive' internal/backup/backup.go                          # 1 hit (line 777)
grep -n 'rootCmd.AddCommand(seedCmd)' cmd/root.go                                # 1 hit (line 523)
grep -n 'return store.SetSetting("config_blob"' internal/config/persistence.go  # 1 hit (line 884)
grep -nE '\{"(sess|invite|apikey|u|abs_sess|idx):"' internal/database/keyfamilies.go   # 6 hits
grep -n 'ABK_ALLOW_ENCRYPTION_KEY_REGEN' cmd/root.go                             # 1 or more
```

05-backup B7 cites `internal/backup/backup.go:1006-1021,1257-1268` at `748fd787a` for the two `.sha256` deletes; at
`51a2c2acd` they are lines 1016–1021 (`DeleteBackupIn`) and 1263–1268 (retention). A miss: STOP.

## Contracts

**Manifest sidecar** (05-backup B7): beside every archive `internal/backup` creates, write
`<archive>.manifest.json` = `{"format":"legacy-abo/0","binary_id":…,"install_id":…,"created_at":…}` (temp file +
fsync + rename, like `writeChecksumSidecar`), where `binary_id` is the running binary's `buildid.Binary` (read
through PC-02-01's process-wide binary ID) and `install_id` the store's `c/storage/install` value. Delete it in the
same two places that delete `<archive>.sha256`. It matches no archive extension, so listing and retention ignore
it as they ignore `.sha256`.

```go
// internal/backup (names coined here except the JSON keys, which are B7's)
type LegacyManifest struct {
	Format    string    `json:"format"`     // "legacy-abo/0"
	BinaryID  string    `json:"binary_id"`
	InstallID string    `json:"install_id"`
	CreatedAt time.Time `json:"created_at"`
}
func ManifestSidecarPath(archivePath string) string // archivePath + ".manifest.json"
func ReadManifestSidecar(archivePath string) (LegacyManifest, error)
```

**`audiobook-organizer staging-restore --archive <file> --db <empty path>`** (09 §9.3 step 1; 05-storage row
"Staging marker"), in this order, refusing with a message and writing nothing at each check:
1. `<archive>.manifest.json` missing → refuse ("an archive fetched without its sidecar cannot be staged").
2. Manifest has no `binary_id` → refuse as a pre-S0 archive ("take a fresh archive on a host that ran S0").
3. `binary_id` ≠ the running `buildid.Binary` → refuse, naming the `binary_id` to run.
4. `--db` exists and is non-empty → refuse. `<db parent>/config.yaml` exists → refuse.
5. Verify the archive against its `.sha256` (HEAD's `verifyChecksumSidecar`) and extract into `--db` with HEAD's
   `extractArchive` logic. Never write `config.yaml`; boot nothing.
6. Read-only open: no `c/storage/install` → remove what step 5 extracted, refuse (pre-S0 store).
7. Guard-aware read-write open, commit `c/storage/staging` = `StagingMarker{ArchiveSHA256, Nonce (32 hex from
   crypto/rand), BinaryID = buildid.Binary, At}`, close cleanly; **then** write the sidecar `<db>.staging` with the
   same JSON (temp file + fsync + rename + directory fsync).

**`audiobook-organizer staging-scrub --db <copy> --fields <file> --confirm-destroy-credentials`** (09 §9.3 step 2):
before any read-write open it reads `<db>.staging`, then the key `c/storage/staging` through a Pebble read-only
open (`pebble.Options{ReadOnly: true}`, which writes no `OPTIONS-<n>`), and refuses, writing nothing, unless both
exist, agree on `archive_sha256`, `nonce` and `binary_id`, that `binary_id` equals its own `buildid.Binary`, and
`--confirm-destroy-credentials` is given. Then it opens the store with `database.NewPebbleStore` and:
- (a) blanks, in the `config_blob` setting, every field `--fields` lists, and deletes every `setting:<name>` row
  whose name it lists and whose row is `IsSecret` (the four encrypted rows of 05-config §2.2);
- (b) deletes every key under `sess:`, `idx:sess:user:`, `abs_sess:` and `invite:` and every one-time token row
  (at `51a2c2acd` none is stored: temp-login tokens live in memory, `tempLoginTokens` in
  `internal/server/auth_temp_login.go`; delete any family your base registers for one); replaces every `u:` row's
  password hash with a value no password matches (an empty bcrypt-incompatible string such as `!` — choice coined
  here); sets every `apikey:` row's status to `revoked` with `revoked_at` set; keeps e-mail addresses (`u:`,
  `oauthid:`, `inviteallow:`);
- (c) clears every legacy integration URL in the blob, a list kept in `cmd/staging_scrub.go` as
  `legacyIntegrationURLFields` (name coined here): at least `deluge_web_url`, the Deluge entry of `download_client`,
  `openai_base_url`, `whisper_remote_url`, every `whisper_endpoints[]` and `ai_endpoints[]` URL, the embedding
  `base_url`, every `metadata_sources[].base_url`, the webhook plugin's URL and the SMTP fields the blob carries;
  a test asserts every listed path exists in `config.Config`'s JSON;
- (d) leaves schedules running and sets `auto_update.enabled` to false.
It writes only those keys.

**`scripts/staging/secret_fields.txt`** (05-config §2.2: "written by hand from this table, every row, the '—' rows and
`plugins[].settings` included"). Format (coined here): `#` comments, one JSON path per line, `.` between object
keys, `[]` for every element of an array, a map path meaning every value of the map. The lines are, in table order:
`openai_api_key`, `google_books_api_key`, `hardcover_api_token`, `basic_auth_password`, `acoustid_api_key`,
`oauth_github_client_secret`, `oauth_google_client_secret`, `deluge_web_password`,
`download_client.torrent.deluge.password`, `metadata_sources[].credentials`, `plugins[].settings.secret`
(webhook HMAC key), `plugins[].settings`, `abs_jwt_secret`, `smtp_password`, `cf_policy_api_token`, and the
four fields of the table's userinfo row: `openai_base_url`, `whisper_remote_url`, `ai_endpoints[].url`,
`embedding.base_url` (19 lines).

**`scripts/staging/provision.sh`** (09 §9.3 steps 1–3; POSIX `sh`): inputs `ARCHIVE` (copied with its
`.manifest.json`), `REF_HOST` and `DEPLOY_BIN` (to fetch the binary whose `binary_id` the manifest names from
`<DEPLOY_BIN>.d/<binary_id>`, or `.prev`, or the current binary, by comparing `--version-json`; read-only copy, never a
rebuild; stop when none matches), `WORK` (an empty directory). It runs `staging-restore`, then `staging-scrub` with
`scripts/staging/secret_fields.txt` and `--confirm-destroy-credentials`, then boots the copy in
`docker run --network none -e ABK_ALLOW_ENCRYPTION_KEY_REGEN=1 …` publishing no port, and prints how to seed test
secrets through `docker exec`. It refuses to run on a non-linux/amd64 host.

**`scripts/boot_budget.py --prev <bin> --cur <bin> --db-copy <dir> --runs 5`** (09 §9.3 "Boot-time budget"):
copies the scrubbed copy per run, boots the binaries interleaved A/B/A/B in a `--network none` container,
discards the first boot of each, records restart → `"ready":true` on `/health` and `warmup_ms`, prints the p50s and
the verdict `p50(cur) ≤ max(1.10 × p50(prev), p50(prev) + 15 s)`, and with `--append <file>` appends a Markdown row
to that stage-record file (option coined here). Its docstring documents the staging preparation (step 1–3 above).

## TDD tests to write first

Spec-named: `TestStagingRestoreWritesMarker`, `TestStagingScrubRefusesUnmarkedStore` (05-storage row "Staging
marker"), `TestStagingRestoreRefusesOtherBinary` (05-backup §5B.9: another binary's archive and a pre-S0 archive
are refused before extracting, naming the `binary_id` or the fresh-archive remedy; an archive without its sidecar
is refused; retention and `DeleteBackupIn` delete the manifest sidecar with the `.sha256`; `staging-scrub` run by
another binary refuses with nothing written) — all in `cmd/staging_test.go` except the backup half in
`internal/backup/manifest_sidecar_test.go`.

`scripts/tests/test_staging_scrub.py`, the **S0 cases** of 09 §9.3 (each a test method; it builds the binary once,
plants a production-shaped fixture archive with every credential kind, runs `staging-restore`, `staging-scrub`
with the archive's binary, then boots it with `serve` on a temp port and checks through the API and a read-only
open): no secret-class blob field, session, one-time token, usable password hash, API key or legacy integration
URL survives the first boot; a `config.yaml` beside the copy holding an OpenAI key makes provisioning refuse and no
key survives the first boot; `staging-restore` refuses a non-empty target; sidecar and key hold one archive hash
and one nonce; `staging-scrub` refuses an unmarked store, a store whose sidecar and key disagree, and a run without
`--confirm-destroy-credentials`, and in each case the store's bytes and sidecars are unchanged (compare a recursive
SHA-256 of the directory before and after); a binary whose `buildid.Binary` differs from the archive writer is
refused by both subcommands (build a second binary with a different `-X main.version`); a pre-S0 archive is refused
by `staging-restore`, writing nothing.

## Steps

1. Re-verify. Write the failing tests.
2. `manifest_sidecar.go`; call it from `CreateBackup` and `CreateBackupWithCheckpoint` right after the checksum
   sidecar; delete it next to both `.sha256` deletes (best effort, same log style).
3. `staging_restore.go`, `staging_scrub.go`; register both with `rootCmd.AddCommand` after `seedCmd`.
4. `secret_fields.txt`, `provision.sh`, `boot_budget.py`.
5. Headers; `changelog.d/` fragment carrying release-note item 7 of 09 S0 "User-visible changes".

## Acceptance

```bash
go test ./cmd/ -run 'TestStagingRestoreWritesMarker|TestStagingScrubRefusesUnmarkedStore|TestStagingRestoreRefusesOtherBinary' -count=1
                                                    # ok  github.com/falkcorp/audiobook-organizer/cmd
go test ./internal/backup/ -run 'Manifest' -count=1 # ok  github.com/falkcorp/audiobook-organizer/internal/backup
python3 -m unittest scripts/tests/test_staging_scrub.py -v   # every test "ok"; final line "OK"
sh -n scripts/staging/provision.sh && python3 scripts/boot_budget.py --help >/dev/null && echo ok   # ok
grep -c '^[a-z]' scripts/staging/secret_fields.txt  # 19
make ci; echo "exit=$?"                             # exit=0
```

## Invariants and STOP conditions

- `staging-scrub` writes nothing (not even an `OPTIONS-<n>`) unless every marker check passes.
- The marker key is committed before the sidecar is written; a production store never gets a marker.
- STOP if PC-02-01's guard-aware open cannot write `c/storage/staging` through `storage/keys`, or if any scrub step
  would need to write a key outside its list.

## Out of scope

The S2b scrub through `app.OpenOffline` and the S7 `core.integrations.disabled` case; `TestSecretClassListMatchesLegacyStore` (S3).

## Rollback

Revert the PR. Older builds ignore `<archive>.manifest.json` (no archive extension) and never see a marker.

## Repo rules

- Worktree only; never `go work init`. Headers on every new or changed file; one `changelog.d/` fragment.
- Every commit ends with `Stage-Release: S0 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr merge <n> --rebase` after the required checks pass (this PR touches `cmd/**` and `internal/backup/**`, so
  `unit-lifecycle` runs once it exists); never squash or `--admin`.
- Concurrency (CLAUDE.md): the scrub walks whole key families (`sess:`, `u:`, `apikey:`) with one sequential
  iterator and batched writes; it is an offline one-shot on a copy, so no worker pool is wanted, and say so in a
  comment.

## Planner notes

- The S0 test text says no "session" survives; this brief deletes `abs_sess:` as well as `sess:`, which 09 §9.3 (b)
  does not list by name. Confirm or narrow at review.
- The format of `secret_fields.txt` and the never-matching hash value are coined here; S3's
  `TestSecretClassListMatchesLegacyStore` must compare against the same format.
