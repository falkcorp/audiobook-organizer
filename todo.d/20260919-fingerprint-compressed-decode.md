- [ ] **FP-COMPRESSED-DECODE** Stored acoustic fingerprints are decoded wrong:
      `BookFile.AcoustIDFingerprint` and `AcoustIDSeg0..6` hold fpcalc's (or the
      ffmpeg chromaprint muxer's) COMPRESSED output, but `decodeAnyFingerprint`
      (`internal/fingerprint/fpcalc.go`) base64-decodes it and reads the bytes
      as a 4-byte header plus little-endian uint32 frames. Compressed
      Chromaprint is `[algorithm][24-bit frame count][bit-packed deltas]`, so
      the "frames" are compressed bitstream. Evidence (2026-09-19, fpcalc
      1.6.1, 30 s test clip): `fpcalc -json` gave 436 bytes with header
      `010000dd` (221 frames); read as uint32s that is 108 fake frames, while
      `fpcalc -raw` gives 221 real frames. Effects: `MinUsefulFingerprintFrames`
      counts fake frames; Hamming similarity (`WholeFileSimilarity`,
      `HammingSimilarity`, `Subprints`/LSH, fuzzy lookup, book signatures)
      compares bitstreams that diverge after the first differing frame, so only
      byte-identical prints match reliably. The 16 consumer sites in section
      (a) of `.claude/notes/windowed-fingerprint-design-2026-09-12.md` all read
      this form. Windowed prints (`fingerprint.FileWindow`) use `fpcalc -raw`
      and are real frames, so they must never be compared with the legacy
      bytes. Fix options for the owner: decompress on read (port
      Chromaprint's decoder), or re-fingerprint with `-raw` and rebuild LSH.
      Either one changes every stored comparison, so decide before PR 8 of the
      windowed design.
