<!-- file: .claude/notes/fix-fingerprint-decompress-v2-progress.md -->
<!-- version: 1.1.0 -->
<!-- guid: 5d0c7a2e-3b41-4f7e-9a6c-2e8b1f4d7c90 -->
<!-- last-edited: 2026-09-25 -->

# fix/fingerprint-decompress-v2 progress

Done:
- STEP 1: decode fix ALREADY MERGED (#3453, 306de1cab, ancestor of proposed-main);
  era versioning eeacc6626 and legacy re-fingerprint selection b30fc926a followed.
  Frozen golden test exists (internal/fingerprint/testdata/chromaprint_golden.json).
- Threshold inventory + fpidx findings gathered.

- Live fpcalc test added + passing (469cd8428).
- Recalibration plan doc, todo.d + changelog.d fragments written.

Next:
- make ci; final report.
