### 🐛 `book_file.missing` / `file_exists` are stale — nothing maintains them

Measured 2026-08-17 against prod: all **2,077** `book_file` rows belonging to the
60 fully-broken books reported `missing: false` and `file_exists: true` via
`GET /audiobooks/:id/files`, while `maintenance.missing-file-audit` proves every
one of those paths fails `os.Stat`.

They are stored columns, not live checks, and no writer keeps them current.

- **Do not filter on them.** Any query that treats `missing = false` as "the file
  is there" is silently wrong, and would report a fully-broken library as healthy.
- Audit who reads them before deciding the fix — either maintain them (the audit
  op already computes exactly this and could persist it) or remove them so nothing
  can be misled.

Found while measuring signal coverage for the missing-file repoint work; see
`docs/audits/2026-08-17-missing-file-audit-full-population.md` §9.
