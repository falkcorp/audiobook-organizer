- Fixed a write-back loop that rewrote audio files on every run. MP4 freeform atoms carry a
  four-byte data header that leaked into the value, so `SERIES_INDEX` read back as
  `"\x00\x00\x00\x004"`; `strings.TrimSpace` does not strip NUL, so the numeric parse failed
  and the field stayed 0. The write landed, the read said "missing", and the next run wrote
  again — forever. Raw tag values are now cleaned centrally. Also fixed three copies of a
  bare `.(int)` assertion on `series_index`/`year` that silently dropped the tag when a
  caller passed a string or JSON float.
