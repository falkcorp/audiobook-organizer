<!-- file: docs/specs/2026-07-10-community-fingerprint-index-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: d75b9f4a-e871-4391-8371-885320cb6f76 -->
<!-- last-edited: 2026-07-10 -->

# Community Audiobook Acoustic-Fingerprint Index — Design Spec

**Status:** Draft — STOP-FOR-HUMAN review required
**Scope:** New product (external GitHub repo + Actions + organizer export/import ops). NO code in this repo, NO new repo, NO external publication until a human approves this spec.
**Parent task:** INIT-8 (`.claude/notes/2026-07-10-remaining-work-master-plan.md` §INIT-8)

---

## Motivation

MusicBrainz/AcoustID model audiobooks poorly: their database is song/recording-based, and a
9-hour multi-file book is not a "recording". Measured consequence (Experiment 0 in
`docs/specs/2026-06-13-dedup-tuning-dataset-design.md`): **recording_id oracle coverage was
0 / 9,842 books (0.0%)** — the AcoustID online lookup has never returned a usable MusicBrainz
recording_id for this library. Every fingerprint→identity mapping we have was earned locally by
human verification, and it currently lives only on one prod box (172.16.2.30).

We already have the acoustic substrate:

- `internal/fingerprint/book_signature.go:48` — `SynthesizeBookSignature(files []FileSegmentData)
  (string, int, error)` concatenates each file's 7-segment chromaprint, max-pools to a fixed
  4,096-word signature (16 KiB raw, ~22 KiB base64 — `BookSignatureFixedLength`), and
  `BookSignatureSimilarity` (line 131 per the verified anchor) compares two signatures by
  normalized Hamming distance. Partial-coverage variants (`SynthesizePartialBookSignature`,
  masked similarity) exist in the same file.
- The labeled-dataset loop (`docs/specs/2026-06-13-dedup-tuning-dataset-design.md`, sub-project
  #1) produces human-verified book identities and explicitly names this index as the "separate
  future track" (#7) that exports from it.
- TODO.md:617-636 (§"Needs Serious Planning — Open Audiobook Acoustic-Fingerprint Index")
  captures the vision and the constraint that shapes everything: **no public server, no hosting
  budget** — a GitHub repository + GitHub Actions is free, durable, and world-pullable.

Re-verify anchors (mandatory — line numbers drift):

```bash
grep -n 'func SynthesizeBookSignature' internal/fingerprint/book_signature.go
grep -n 'func BookSignatureSimilarity' internal/fingerprint/book_signature.go
grep -n 'func SynthesizePartialBookSignature' internal/fingerprint/book_signature.go
grep -n 'func BookSignatureSimilarityMasked' internal/fingerprint/book_signature.go
grep -n 'RunItems' internal/plugins/acoustid/backfill.go
grep -n 'dedup-tuning-dataset-design' TODO.md
grep -n 'Needs Serious Planning' TODO.md
```

**Goal:** design a community-owned, Git-repo-backed "AcoustID for audiobooks" — verified
audiobook acoustic fingerprints + identity metadata — that gives us disaster recovery,
distribution, and provenance, and supersedes AcoustID submission for audiobooks.

## Goals

- **Disaster recovery (first-class).** If prod data is wiped, the organizer rehydrates its
  identity layer (fingerprint → title/author/series/narrator/edition) by pulling the repo.
  The index must round-trip: everything needed to re-identify a local file tree lives outside
  our box.
- **Provenance (first-class).** Every record change is a reviewable commit/PR. A record's
  history — who submitted it, how it was verified, when it was challenged — is the git history.
- **Distribution.** Anyone can clone the repo (or download a derived artifact) and skip the
  manual fingerprint/identify work we do today.
- **Mergeability.** Concurrent community PRs must not conflict structurally; format and layout
  are chosen for clean git merges.
- **Zero hosting budget.** Public GitHub repo + Actions + Releases + raw file access only.

## Non-goals (v1)

- A hosted query API / web service — deferred; consumers clone or fetch derived artifacts.
- An AI-queryable representation (embeddings over the index) — named in TODO.md as a
  possibility; deferred until the core index exists.
- Automated (non-human-verified) record submission — v1 accepts only human-verified records.
- Music/podcast content — audiobooks only.
- Real-time lookup during import — v1 consumption is batch (pull + local match).

## Decisions (locked during design — each names the losing options)

The master plan (INIT-8, `.claude/notes/2026-07-10-remaining-work-master-plan.md`) enumerates
five design-question clusters — on-disk format, the PR-bot loop, identity unit,
trust/governance/license, and the relationship to AcoustID. D1 and D3–D6 answer those five.
D2 (repo + Actions architecture) is not a sixth payload question; it is an in-scope
sub-question of the master plan's locked storage mandate ("stored in a **GitHub repo +
Actions** (no hosting budget)"), answered here because every other decision depends on it.

### D1. On-disk format — sharded JSONL (canonical) + derived artifacts on Releases

**Chosen: sharded, canonicalized JSONL.** One record per line; records serialized with sorted
keys and no insignificant whitespace; one shard file per 2-hex-char prefix of the record ID
(256 shards, `shards/ab/records.jsonl`); lines within a shard sorted by `record_id`. CI
enforces canonical form (sorted, formatted) on every PR **before** merge (see D2 — never via a
bot push to main), so two PRs appending different records to the same shard merge cleanly or
trivially rebase. Bulky acoustic payloads (see D4) are
size-bounded so a shard stays well under GitHub's 50 MB file warning.

Derived artifacts — a single Parquet file and a SQLite snapshot — are **built by Actions on
merge and attached to GitHub Releases**, never committed. Consumers who want fast columnar
scans use the release artifact; the repo itself stays diffable. *(Deferred at v1 — no derived
artifact is built until a named consumer exists; see "V1-minimal core vs deferred-scale
mechanisms".)*

**Losing options:**
- **Parquet as the canonical store** — fails the hard requirement (mergeability): binary,
  un-diffable, every append rewrites the file, git merge impossible. Kept only as a derived
  release artifact.
- **Checked-in Pebble/SQLite snapshot** — same binary-blob problem, plus every update re-adds
  the whole snapshot to git history. This repo already learned that lesson: audiobook-organizer
  itself is 1.69 GB from committed binary fixtures (REPO-SIZE-1, #1650).
- **One file per record** (maximum mergeability) — wins on merges but loses at scale: 100K+
  records = 100K+ inodes, slow clones/checkouts, git tree-object bloat, and GitHub UI/API
  pagination pain. 256-way JSONL sharding gets ~99% of the merge benefit at ~0.3% of the
  file count.

### D2. GitHub repo + Actions architecture (no hosting budget)

**Chosen: one public repo (working name `falkcorp/audiobook-fingerprint-index`), 256 shard
dirs, schema + validator + CI in-repo, derived artifacts on Releases.**

```
audiobook-fingerprint-index/
├── LICENSE                      # CC0-1.0 (D5)
├── README.md                    # what it is, how to consume, how to submit
├── GOVERNANCE.md                # maintainers, challenge/revert process (D5)
├── schema/
│   ├── record.schema.json       # JSON Schema for IndexRecord (versioned)
│   └── CHANGELOG.md
├── shards/
│   ├── 00/records.jsonl … ff/records.jsonl
├── works/
│   └── works.jsonl              # work-cluster records (D4), small, single file — DEFERRED at v1
├── tools/
│   └── validate/                # Go validator CLI (schema + canonical form + dedup)
└── .github/workflows/
    ├── validate-pr.yml          # every PR: schema, canonical form, near-dup, size caps
    ├── canonicalize.yml         # PR-branch only: fail PR if not canonical — NEVER direct-pushes main
    └── release-artifacts.yml    # on merge to main: build Parquet + SQLite — DEFERRED at v1
```

**CI validation on every PR (all free on a public repo):** JSON-Schema validation of every
added/changed line; canonical-form check (sorted keys, sorted lines, correct shard for the
record_id prefix); **near-duplicate check — corpus-wide, never shard-scoped.** The shard is
derived from a SHA-256 prefix (D4), which has no acoustic locality: a re-encode/bitrate variant
produces a near-identical but not bit-identical signature, hashes to a different record_id, and
lands in a uniformly-random different shard ~255/256 of the time — so a same-shard scan would
degenerate to exact-signature matching and the ≥0.95 similarity gate would never fire.
Therefore CI decodes each new record's whole-book signature and compares it against **every
existing record across all shards**, sharding the OUTER loop across a bounded worker pool per
the concurrency house rule. **Comparator selection (must match C4's import side):** the plain
`BookSignatureSimilarity` is used only when BOTH records have `coverage_pct == 100`; whenever
the incoming OR the existing record is partial (`coverage_pct < 100` / `signature_mask`
present), CI uses `BookSignatureSimilarityMasked` over the intersected masks, with a
minimum-overlap-word floor — below the floor the comparison is inconclusive and the PR fails
with "insufficient overlap: needs human review". Without this rule a partial-coverage record
(explicitly permitted by the D4 schema) could evade or falsely trip the ≥0.95 gate via
distorted unmasked Hamming distance, gutting D5's poisoning layer (c). At v1 seed scale (<10K records) this is seconds of
work; at the 150K federation ceiling it is ~150K comparisons per new record × ≤500 records/PR,
still tractable on Actions runners with the pool. When the full-corpus scan becomes the CI
bottleneck, the deferred mechanism is a locality-preserving LSH band index over signatures —
the organizer already runs exactly this Hamming-similarity LSH store at ~275K fingerprints;
port its banding scheme — rebuilt on merge as a derived artifact/cache, never committed to main
(see "V1-minimal core vs deferred-scale mechanisms"
below). Also enforced: batch cap (≤ 500 records per PR); provenance fields present; license
attestation present. Actions are SHA-pinned per house rules.

**Canonical form is enforced PRE-merge, never by a bot push to main.** `canonicalize.yml` runs
on the PR branch and fails the PR if any touched shard is non-canonical (the submitter — or the
workflow, pushing a fixup commit to the PR branch itself — re-canonicalizes and re-runs). It
never direct-commits to main: D5's branch protection (all merges via PR, required CI, linear
history) binds bots too, so no CI identity holds bypass-branch-protection rights, and a
canonicalization bug can never auto-commit corrupted shards to the canonical data without a
human on that write. If post-merge drift is ever detected (e.g. after a workflow-logic change),
the remediation is a bot-opened *reviewable PR*, merged by a maintainer like any other change.

**Size/scale limits (the honest math).** GitHub recommends repos < 1 GB and warns near 5 GB;
individual files hard-fail at 100 MB. A v1 record (D4) is ~24–40 KiB dominated by the ~22 KiB
base64 whole-book signature, and base64 of high-entropy fingerprint data does not
zlib-compress, so git stores it near raw size. Capacity ceiling:

| Records | Repo working set | Verdict |
|---|---|---|
| 10K (our verified core today is smaller) | ~0.3 GB | comfortable |
| 50K (~our whole library: 44K books) | ~1.5 GB | fine, past the 1 GB "recommended" line |
| 150K | ~4.5 GB | at the 5 GB warning — federation trigger |
| 1M (community success) | ~30 GB | impossible in one repo |

**Escape hatch (designed now, built later):** a `manifest.json` at the repo root lists shard→repo
assignments; when the 150K trigger fires, shards split into sibling repos
(`…-index-shard-0` … ) and the manifest federates them. Consumers read the manifest first.
History growth is bounded by records being append-mostly (edits are rare, human-verified).

**Losing options:** Git LFS (free tier is 1 GB storage / 1 GB-month bandwidth — worse than
plain git for world-pullable data); GitHub Pages as the primary store (1 GB site cap, no PR
provenance); multiple repos from day 1 (premature — federation complexity before 150K records
buys nothing).

### D3. The PR-bot loop — app emits PRs of human-verified records; CI validates; humans merge

**Chosen flow:**

1. **Export op in the organizer** (future `communityindex.export-verified`, registered like
   other plugin ops): selects books whose identity is human-verified (per the labeled-dataset
   loop, `LabelSource="human"` / verified records from
   `docs/specs/2026-06-13-dedup-tuning-dataset-design.md`), synthesizes each book's
   `IndexRecord` (D4), and diffs against a local clone of the index. **Dry-run first** (prints
   would-add / would-update counts), then a **real AskUserQuestion apply gate** before any PR
   is opened — this is an external publication of library-derived data.
2. **Batch + branch:** ≤ 500 records per PR; the op writes shard lines into a fresh branch of a
   local clone and pushes via native git + `gh pr create` (NEVER the MCP contents API — house
   rule; workflow files are never touched by the bot at all).
3. **CI validates** (D2 checks). A near-dup hit against an existing record flips that line from
   "add" to "needs merge-into-existing" and fails the PR with the existing `record_id` named —
   the submitter re-runs the export, which then emits an *edit* of the existing record
   (provenance appended) instead of a new one.
4. **Merge policy:** maintainer review + `gh pr merge --rebase` (rebase/FF only, matching org
   convention). *Terminology note:* the master plan (§INIT-8) phrases this loop as "CI that
   validates and applies" — here "apply" == the maintainer's rebase-merge of a CI-green PR
   (CI gates, humans apply). That is a phrasing refinement, not a scope change. After a
   submitter has N clean merged PRs, a `trusted-submitter` allowlist enables
   auto-merge-on-green for pure-append PRs; edits/deletes always need human review. Auto-merge,
   when eventually enabled, stays append-only + batch-capped and adds a **spot-check/cooldown**:
   a sampled human review of recently auto-merged batches on a fixed cadence, with auto-merge
   suspended for a submitter whose sample fails. *(The allowlist is deferred until M5 — at v1
   the only submitter is us and everything is human-reviewed; and per the M5 milestone, turning
   auto-merge ON is an explicit named item in the M5 human decision, never a default that rides
   M5 activation. See "V1-minimal core vs deferred-scale mechanisms".)*
5. **Import/rehydrate op** (future `communityindex.import`): pulls the repo, matches local books
   by `BookSignatureSimilarity` (masked variant when local coverage is partial), and restores
   identity metadata. Dry-run first + AskUserQuestion gate — it mutates prod book metadata.

**Losing options:** GitHub Issues as the submission queue (no diffable payload, no CI hook on
content); a bot with direct push rights (destroys the provenance story — every record must
enter through a reviewable PR); GitHub App server-side bot (needs hosting — budget is zero).

### D4. Identity unit — whole-book signature keys the record; works cluster records

**Chosen: the record = one acoustic edition.** `record_id` = first 16 hex chars of
SHA-256 over the raw (decoded) whole-book signature bytes — content-addressed, stable,
collision-negligible at this scale, and it defines the shard **for storage and merge purposes
only**. Exact identity (the content hash) and approximate matching are deliberately decoupled:
the SHA-derived shard has no acoustic locality, so it is never used to select near-dup
candidates — candidate selection is corpus-wide (D2).

A record carries three layers:

1. **Acoustic:** the base64 book_sig_v1 whole-book signature (4,096 words, from
   `SynthesizeBookSignature`); the coverage mask + coverage % when synthesized partially
   (`SynthesizePartialBookSignature`); per-part entries with duration, ordering, and a
   **compact per-segment digest** (SHA-256/16-hex of each of the 7 chromaprint segments) rather
   than the full per-part fingerprints — this keeps a 30-file book's record ~24–40 KiB instead
   of 300 KiB+. Full per-part fingerprints are optionally bundled into the Release artifacts
   (built from submitter uploads), never into shard files. *(Losing option: full part fps
   in-repo — blows the D2 size budget ~10×; losing option: whole-book sig only — loses the
   part-level containment signal that distinguishes a part from its whole.)*
2. **Bibliographic:** title, authors, series+index, narrators, language, abridged flag,
   publisher, year, external IDs (ISBN/ASIN/iTunes PID where known).
3. **Provenance:** submitter, client + version, verification method (`human_verified` only in
   v1), timestamps, and a free-text evidence note.

**Editions / abridgements / re-narrations:** different acoustic content ⇒ different signature ⇒
**different record** — a re-narration or abridgement is automatically a new record; no policy
needed. Re-encodes/bitrate variants of the *same* narration produce near-identical chromaprints,
so CI's corpus-wide near-dup check (similarity ≥ 0.95, candidates selected independently of the
content-hash shard per D2, masked comparator when either side is partial — threshold to be
calibrated pre-launch on our own library) maps them onto the existing record instead of adding
a duplicate. For scale intuition only: the organizer's dedup full-scan ran at 606 books/sec on
prod — but that is a *different workload* (ISBN-indexed pairwise candidate generation, not
one-signature-vs-all Hamming), so treat the figure as an order-of-magnitude reference from a
sibling workload, NOT a validated CI benchmark or budget. Records of the same *work* are
clustered by a `work_key` (normalized author+title, overridable by explicit `work_id` in
`works/works.jsonl`), with per-record `edition`, `abridged`, `narrators` fields carrying what
distinguishes them — but the `work_key` field is **deferred alongside `works.jsonl`** (see the
deferred-mechanisms table): its only would-be consumer, the C4 fingerprint→identity round-trip,
needs no work clustering, and as an `omitempty` field it can be introduced later at zero cost.
*(Losing option: keying records on metadata (title+author+narrator) — collides on re-releases
and diverges on tag noise; acoustic identity is the only key that is self-verifying.)*

```go
// IndexRecord is one line in shards/<p>/records.jsonl. Canonical JSON: sorted keys.
type IndexRecord struct {
	RecordID      string   `json:"record_id"`       // 16-hex sha256 prefix of decoded book signature
	SchemaVersion int      `json:"schema_version"`  // 1
	WorkKey       string   `json:"work_key,omitempty"` // DEFERRED at v1 — empty until the works.jsonl layer ships
	Signature     string   `json:"signature"`       // base64 book_sig_v1 (4096 uint32, ~22 KiB)
	SignatureMask string   `json:"signature_mask,omitempty"` // base64 coverage mask when partial
	CoveragePct   int      `json:"coverage_pct"`    // 100 when complete
	Parts         []Part   `json:"parts"`           // ordered
	Meta          BookMeta `json:"meta"`
	Provenance    Prov     `json:"provenance"`
}

type Part struct {
	Ordinal     int      `json:"ordinal"`
	DurationSec int      `json:"duration_sec"`
	SegDigests  []string `json:"seg_digests"` // 7 × 16-hex sha256 of each chromaprint segment
}

type BookMeta struct {
	Title     string   `json:"title"`
	Authors   []string `json:"authors"`
	Narrators []string `json:"narrators,omitempty"`
	Series    string   `json:"series,omitempty"`
	SeriesIdx float64  `json:"series_idx,omitempty"`
	Language  string   `json:"language,omitempty"`
	Abridged  bool     `json:"abridged,omitempty"`
	Edition   string   `json:"edition,omitempty"`
	Publisher string   `json:"publisher,omitempty"`
	Year      int      `json:"year,omitempty"`
	ISBN      string   `json:"isbn,omitempty"`
	ASIN      string   `json:"asin,omitempty"`
}

type Prov struct {
	Submitter    string `json:"submitter"`     // GitHub handle of the PR author
	Client       string `json:"client"`        // e.g. "audiobook-organizer/v0.217.7"
	Verification string `json:"verification"`  // v1: always "human_verified"
	SubmittedAt  string `json:"submitted_at"`  // RFC3339
	Note         string `json:"note,omitempty"`
}
```

### Persistence

- Index repo: `shards/<2-hex>/records.jsonl` → one canonical-JSON `IndexRecord` per line.
- Index repo: `works/works.jsonl` → work-cluster overrides.
- Organizer side (post-approval, future work): `communityindex:exported:<recordID>` →
  `{bookID, exportedAt, prNumber}` in PebbleDB. **Written only on the apply path, after the
  AskUserQuestion gate AND only after `gh pr create` has returned success** (so a failed
  push/PR-create mid-batch leaves no marker and the retry is not suppressed); a dry-run writes
  zero Pebble markers (a dry-run marker would suppress a later real export). **The marker is a
  hint/audit record, NOT the source of truth for idempotency:** the authoritative idempotency
  check is the diff against the local clone of the index itself — a record already present in
  the index is never re-added regardless of markers, and a record whose marker exists but which
  is absent from the index (failed push discovered late, or a PR the maintainer DECLINED) is
  treated as stale: the export reconciles such markers against actual index contents on every
  run and re-surfaces the record in the would-add diff. This keeps the marker and the index
  from ever disagreeing about what has been published (see `TestExportRetryAfterDeclinedPR`).

### D5. Trust, governance, license

**License — chosen: CC0-1.0 for the entire dataset.** Fingerprints are machine-derived
measurements with murky copyrightability; any restrictive license invites compliance doubt that
kills adoption, and the whole point is that the world can use it. *(Losing options: ODbL —
share-alike protects against proprietary capture but its attribution/notice mechanics deter
exactly the downstream tools we want; CC-BY-4.0 — attribution on a per-record dataset is
impractical for consumers.)* Repo code (validator, workflows) is MIT.

**Governance:** maintainers = repo owner + invited co-maintainers, listed in GOVERNANCE.md +
CODEOWNERS; all merges via PR (branch protection: required CI, linear history). **Challenge
process:** a bad record is challenged via an issue template naming the `record_id` + evidence;
resolution is an edit/removal PR — the git history *is* the audit trail, and a revert is one
commit. Deletions leave a tombstone line (`"verification":"retracted"`) so consumers can purge.

**Poisoning resistance (layered, none load-bearing alone):** (a) every record enters via a
reviewed PR; (b) CI structural checks — a signature must decode to exactly 4,096 words, parts
must sum plausibly against metadata duration; (c) near-dup check stops shadow-duplicates of
existing records under different metadata (corpus-wide per D2 — a shard-scoped check would
miss ~255/256 of re-encodes and this layer would be inert); (d) batch caps (≤500/PR) bound blast radius;
(e) new submitters are quarantined (first PRs always human-reviewed; `trusted-submitter`
earned); (f) provenance pins every record to a GitHub identity; (g) spot-check reproducibility —
a maintainer with the same audio can recompute the signature and compare. Accepted residual
risk: a malicious *trusted* submitter can land plausible-but-wrong metadata until challenged;
mitigated by revert-in-one-commit + tombstones.

### D6. Relationship to AcoustID — supersede for audiobooks; keep submission as optional export

**Chosen (locked by the master plan):** this index **supersedes** AcoustID for audiobooks.
Their recording model fits audiobooks poorly and returns nothing for us (0/9,842 books,
Experiment 0). AcoustID submission remains available as an **optional downstream export** from
the same verified-record stream — a separate, off-by-default op — never a dependency: nothing
in the index's schema, CI, or matching path references AcoustID. **Placement:** that op is NOT
part of the v1 core and NOT part of milestones M1–M5 — it is a deferred mechanism with its own
row and build trigger in the deferred-mechanisms table below, so this spec names no surface it
does not place. *(Losing option: dual-write to AcoustID as a peer store — couples our loop to
an API that models the domain wrongly and adds a failure mode for zero retrieval value.)*

## V1-minimal core vs deferred-scale mechanisms

The decisions above describe the full architecture so the design does not paint itself into a
corner, but the actual v1 is a <10K-record, single-submitter seed (us, exporting our own
human-verified core). **Approving this spec commits to the v1 core only** — each scale
mechanism below is named now, with the trigger that authorizes building it later; none is
built at v1.

**V1 core:** one public repo; sharded canonical JSONL; Go validator + PR CI (full-corpus
near-dup scan); all-human PR review; the D4 record schema (WITHOUT `work_key`, which is
deferred alongside `works.jsonl` — an `omitempty` field addable later at zero cost); C3 export
+ C4 import ops. Single submitter (us) until M5. The 256-way shard scheme IS kept at v1 despite
~40 records/shard, with eyes open about what it does and does not buy now: its
concurrent-submitter merge-conflict benefit does NOT materialize until M5 community opening
(itself a STOP gate), and pre-M5 there are no external consumers whose addressing could break.
What forces the choice at birth is file-size math at the committed federation ceiling: at 150K
records (~30 KiB each), 256 shards ≈ 18 MB per file (comfortably under GitHub's 50 MB warning),
16 shards ≈ 280 MB per file (over the 100 MB hard fail), and a flat file is a single ~4.5 GB
blob — so 256 (the natural 2-hex-char prefix fanout) is the smallest layout that survives to
the trigger without a consumer-breaking re-shard, and its v1 cost is near-zero (a 2-hex prefix
check the validator already computes).

| Deferred mechanism | Trigger to build it |
|---|---|
| Federation — `manifest.json` + sibling shard repos (D2) | 150K records (~4.5 GB working set) |
| Derived release artifacts — Parquet + SQLite (D1) — `release-artifacts.yml` is NOT built at v1 | first named consumer that cannot simply clone the repo |
| LSH band index for near-dup candidate selection (D2) | full-corpus CI scan exceeds a few minutes per PR |
| `trusted-submitter` allowlist + auto-merge-on-green (D3/D5) | M5 community opening + a track record of clean external PRs; enabling auto-merge is an explicit named item in the M5 human decision, never a default; ships with append-only + batch-cap + spot-check/cooldown (D3 step 4) |
| `works/works.jsonl` cross-edition override layer + the `work_key` record field (D4) | a consumer actually consumes cross-edition grouping — the fingerprint→identity round-trip (C4) needs no work clustering; `work_key` is `omitempty`, addable at zero cost |
| AcoustID submission-export op (D6) | explicit owner request, and never before M3 has published the records it would export; off-by-default; not part of M1–M5 |

## Components (all post-approval; named here so the human can judge total surface)

### C1. Index repo scaffolding (`falkcorp/audiobook-fingerprint-index`)
Layout per D2, schema per D4, workflows per D2/D3. **Does not exist until approved.**

### C2. Validator CLI (`tools/validate` in the index repo)
Go CLI: schema check, canonical-form check, shard-placement check, near-dup scan
(`BookSignatureSimilarity` port or `internal/fingerprint` extraction into a shared module —
open question OQ2), batch-cap enforcement. Runs identically in CI and locally.

### C3. Export op (`communityindex.export-verified`, organizer plugin)
Dry-run default; AskUserQuestion apply gate; bounded batches; native git + `gh` for the PR.
Whole-library candidate selection uses `registry.RunItems` (bounded pool per the concurrency
rule — see `internal/plugins/acoustid/backfill.go` pattern), and any book hydration goes
through full-row getters (memdb-slim write-back footgun does not apply — the op is read-only on
book rows — but signature synthesis must read fingerprint fields from hydrated rows).

### C4. Import/rehydrate op (`communityindex.import`, organizer plugin)
The disaster-recovery path: pull repo → match by (masked) signature similarity → restore
identity metadata. Dry-run + AskUserQuestion gate (mutates prod metadata).

**Fail-CLOSED matching.** A local book enters the apply set only when its best community-record
match is (a) at or above a stated similarity floor — calibrated during the seed load alongside
the near-dup threshold; placeholder ≥ 0.98, deliberately stricter than the 0.95 near-dup line
because a false restore silently overwrites correct prod metadata — and (b) unambiguous: no
second candidate within a near-tie margin of the best. Below-floor, tied, or multi-candidate
books are SKIPPED, never guessed; the dry-run reports skip counts by reason (below-floor /
ambiguous / no-match) alongside the would-restore count.

**Write path (memdb-slim footgun — mandatory).** C4 WRITES book identity metadata — the exact
shape that caused the production AcoustIDFingerprint wipe (bare `UpdateBookFile`/`UpdateBook`
round-tripping memdb-slim structs back into Pebble; PR-A #1839 / PR-D #1854; the `UpdateBook`
STOR-1 guard covers only 7/9 heavy fields and NOT Author/Series). C4 must hydrate full rows via
full-row getters and write back through a field-scoped update that touches ONLY the identity
fields being restored — never a full-struct replace. A regression test asserts import does not
clear `AcoustIDFingerprint` or any other heavy field (see Testing).

## Migration / integration

- Upstream source of truth: the labeled-dataset loop
  (`docs/specs/2026-06-13-dedup-tuning-dataset-design.md`) — this index exports only records
  that loop marks human-verified. No changes to that loop are required; the export op reads it.
- `internal/fingerprint/book_signature.go` is consumed as-is; if the validator needs the
  similarity function outside this repo, OQ2 decides copy vs extract.
- No existing organizer behavior changes until C3/C4 ship, and those are new ops (additive).

## Milestones (M0 approval authorizes ONLY M1 — every irreversible milestone re-gates)

**Gate structure:** approving this spec (M0) authorizes **only M1** — scaffold, no data, no
organizer code, fully reversible. Each externally-visible or irreversible milestone — **M3
(first irreversible publish)** and **M5 (community opening)** — is its own STOP-FOR-HUMAN
approval point and must never proceed on the M0 approval alone. **M2 and M4 need no dedicated
STOP gate:** they are ordinary, reversible organizer-side changes (new flag-off ops,
`git revert`-able) and proceed under the normal worktree + PLAN.md + PR plan-approval house
flow once their predecessor milestone is done — M0 neither blocks them at M1 forever nor
pre-authorizes them wholesale; each gets its own routine plan approval. Only M3 and M5 are hard
STOP-FOR-HUMAN re-gates beyond M0. The export op (C3) is built BEFORE any seed data is
published, so the first and largest publish rides the gated dry-run → AskUserQuestion
mechanism, never an ad-hoc export.

- **M0 — this spec.** STOP-FOR-HUMAN review. The only deliverable of INIT-8's current cycle.
- **M1 — repo scaffold + validator + CI** (index repo only; no organizer code, NO data).
  Fully reversible in the sense that matters: **no library-derived DATA has been published** —
  only the CC0/MIT scaffold (schema, validator, workflows) is world-cloneable once the repo is
  public, which carries no library blast radius (matching the Rollback section's line: the
  M1/M2 "delete the repo" statement is an organizer-impact statement). Option for the owner:
  create the repo private until M3 if M1/M2 should be fully invisible — not required.
- **M2 — export op C3** in the organizer (worktree + PLAN.md + PR; dry-run + AskUserQuestion).
  Still zero publication — the op exists but has not been applied.
- **M3 — seed load. FIRST IRREVERSIBLE PUBLISH — own STOP gate.** Preconditions: (a) OQ5
  resolved — explicit owner sign-off on CC0 irrevocability / PII — and (b) a dedicated human
  approval of the seed publish itself, executed through C3's dry-run → AskUserQuestion path
  (never an ad-hoc export). Calibrate the near-dup threshold on known re-encodes before any
  merge. Once ANY seed PR merges to main, the data is world-cloneable CC0 and cannot be
  recalled.
- **M4 — import/rehydrate op C4** + a disaster-recovery drill against a scratch DB.
- **M5 — community opening — own STOP gate:** README/GOVERNANCE public docs, submission guide,
  trusted-submitter policy activation. **Enabling auto-merge-on-green is an explicit, separately
  named item in the M5 human decision** — it does not activate by default with M5; the human may
  approve community opening with all-PRs-human-reviewed and defer auto-merge indefinitely.

## Files modified

| File | Change |
|---|---|
| *(none)* | Spec-only. NO code, NO task briefs, NO repo creation, NO external publication until a human approves. |

## Testing (post-approval, recorded for the future plan)

| Test | Asserts |
|---|---|
| `TestValidateRecordSchema` | good record passes; missing provenance / bad shard / non-canonical JSON fail closed |
| `TestNearDupDetection` | re-encode of an existing record is flagged with the existing record_id **even when the re-encode's record_id lands in a different shard** (corpus-wide scan, D2); distinct narration passes; **when either side has `coverage_pct < 100`, the masked comparator is used** (partial re-encode still flagged; below the overlap floor → held for human review, never silently passed) |
| `TestRecordIDDerivation` | record_id = 16-hex sha256 prefix of decoded signature; stable across re-serialization |
| `TestExportDryRunNoSideEffects` | dry-run opens no PR, writes no branch, writes **zero Pebble markers** (`communityindex:exported:*`), prints exact counts |
| `TestExportMarkerOnlyAfterPRCreate` | a failed push / failed `gh pr create` mid-batch writes NO marker for the unpushed records; the next export run re-includes them in the diff |
| `TestExportRetryAfterDeclinedPR` | a marker whose record is absent from the index (declined PR) is reconciled as stale; the record re-surfaces in the would-add diff instead of being suppressed |
| `TestImportRoundTrip` | export → wipe scratch DB identity fields → import → identity restored (disaster-recovery drill) |
| `TestImportFailClosed` | below-floor / near-tied / no-match books are skipped and counted, never applied |
| `TestImportPreservesHeavyFields` | import apply touches only restored identity fields; `AcoustIDFingerprint` and all heavy fields byte-identical before/after |

## Rollback

**Gate (verbatim):** STOP-FOR-HUMAN. New-product blast radius. Spec only; NO code, NO task
briefs, NO repo creation, NO external publication until a human approves. The only 'task' is
AWAIT-APPROVAL.

Rollback posture per milestone (post-approval): **the irreversibility boundary is M3 (first
merged seed PR), not M5.** Through M1/M2 everything is reversible — delete the index repo,
revert the C3 PR (flag-off op), zero publication. During M3, up to the moment a seed PR merges,
rollback is still total: decline the PR / delete the not-yet-merged branch. **Once ANY record
merges to main, the data is public CC0 — cloneable and forkable — and archiving or deleting the
repo does NOT un-publish it**; from that point "rollback" means tombstoning records, never
un-publishing. Separately, *organizer-side* impact stays reversible throughout: C3/C4 are new,
off-by-default ops that land flag-off behind their own PRs and revert with `git revert` at any
milestone. "Archive/delete the repo with zero impact" is an organizer-impact statement only —
it is NOT a publication-reversibility statement once M3 has merged anything.

## Open questions (for the human reviewer — NOT resolved)

1. **OQ1 — repo home:** `falkcorp` org vs a neutral community org? Affects perceived neutrality
   and long-term governance.
2. **OQ2 — code sharing:** recommended: **copy** `BookSignatureSimilarity` (+ decode helpers)
   into the index repo's validator — for v1 this dominates (one call site, a small pure
   function; drift is caught by cross-testing against known signature fixtures). Extracting
   `internal/fingerprint` into a shared public Go module is a real refactor of this repo's
   internals and is **deferred unless a second consumer appears**; approving this spec does
   NOT implicitly approve that carve-out.
3. **OQ3 — near-dup threshold:** 0.95 is a placeholder; calibrate on our library's known
   re-encodes during M3 (seed load) before any community PR is accepted. The C4 restore floor
   (placeholder 0.98) is calibrated at the same time.
4. **OQ4 — seed scope:** publish only human-verified records (strict, small) vs also
   auto-high-confidence records marked as such (bigger seed, weaker provenance)?
5. **OQ5 — PII/rights review:** confirm publishing fingerprints + metadata derived from a
   personal library is acceptable to the owner (CC0 is irrevocable). **Must be resolved BEFORE
   M3 — it is a named precondition of the first irreversible publish (see Milestones).**
