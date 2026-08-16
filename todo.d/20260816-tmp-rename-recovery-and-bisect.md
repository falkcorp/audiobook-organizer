### Stranded `.tmp-rename` recovery — bisect complete, recovery outstanding

~35 GB of audio is stranded on disk inside directories that a path-construction
bug created. The bisect is **done**; recording it so nobody re-derives it.

**Introduced:** `f29c3ce6`, 2026-03-03 13:43:40 —
`viper.SetDefault("segment_title_format", "{title} - {track}/{total_tracks}")`.
A shipped default with a literal `/` in it, so `{track_title}` expanded to
`"Pink Bean Series - 1/9"` with every variable value perfectly clean.

**Removed:** `c54721c7`, 2026-08-15 22:10:50 — deleted `segment_title_format`
outright and defaulted the file pattern to `{title} - {track:02d}`.

Live for 5.5 months. `243e2f38` ("scrub path separators from template
variables", 2026-05-28) is **not** the fix for this one — `scrubVar` sanitizes
variable values, and this separator was in the template. It fixed a genuinely
separate bug (title metadata containing `3/85`) whose on-disk wreckage is
identical, which is why the two were conflated.

**Measured on prod 2026-08-16 (read-only):**

| metric | value |
|---|---|
| stranded `.tmp-rename` files | 2,584 |
| bogus directories | 2,535 |
| affected books | 82 |
| books with no other copy | **77** |
| size | 35.2 GB |
| bogus-dir mtime range | 2026-04-07 → 2026-04-30, plus **2 on 2026-08-14** |

Directory mtime is the right instrument, not file mtime: `rename(2)` preserves
file mtime, so the files carry inherited dates, while the bogus directory only
exists as a product of the bug. `mtime == ctime` on all 2,535.

The two 2026-08-14 directories came from the `internal/metafetch` twin, which
had no `scrubVar` at all until #2479 unified the builders — so the live
metadata-apply path was still stranding files two days before that landed.

**⚠️ The `.tmp-rename` census is an undercount.** The bogus directories also
contain *successfully* renamed files (`Project Hail Mary - 16/31.mp3` sits
beside `Project Hail Mary - 24/31.mp3.tmp-rename`). Any recovery must sweep the
directories, not just the `.tmp-rename` glob.

**Outstanding:**

- [ ] Recovery tool. Dry-run by default, full report before anything moves.
      **77 books have no other copy — a wrong move is unrecoverable.** Derive
      and validate the naming rule against the 5 books that also contain
      surviving audio (Project Hail Mary, Singularity Online 1, Welcome to the
      Multiverse 5, Dreamcatcher, Neuromancer) before pointing it at the other
      77. Reconstruct by rejoining directory tail + filename
      (`"Pink Bean Series - 1" + " " + "9.m4b"`), not by relocating the bare
      file, which discards the chapter's identity.
- [ ] Compare with per-file SHA-256; where hashes differ because of embedded
      artwork, fall back to `ffmpeg -v error -i FILE -map 0:a -f md5 -`, which
      hashes decoded audio and ignores container metadata. Exact, unlike
      AcoustID — only exact should authorize a delete.
- [ ] Investigate book rows affected as a side effect. `scrubVar`'s own comment
      records the scanner reacting by creating **85 separate Book records** for
      one book, so look for spurious rows (path segment matching ` - [0-9]+$`,
      or a purely numeric title) *before* doing soft-delete/purge archaeology.
- [ ] Confirm no new bogus directories appear now that both the pattern and the
      builder guard are in place. The post-fix observation window is currently
      about zero.
