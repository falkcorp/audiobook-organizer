### Fixed

- iTunes import no longer creates author rows out of chapter-file numbering.
  Track and chapter tags such as `001_Celestia`, `Track 01` and
  `000m_00s__056m_16s_43h` were being stored as authors. The new gate strips the
  numbering rather than rejecting outright, so a tag like
  `001-147 Kevin J Anderson` still resolves to the real author instead of being
  discarded, and merges into the existing row for that person.
