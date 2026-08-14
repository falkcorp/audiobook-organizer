- [ ] 🔍 **`/api/v1/audiobooks` answers `count: 0` for `marked_for_deletion=true` while
      3,953 soft-deleted books exist.** Measured on prod 2026-08-14 ~01:20 EDT, straight
      after the PR #2392 deploy:

      | request | count |
      |---|---|
      | no filter | 63,869 |
      | `filters=[{"field":"marked_for_deletion","value":"true"}]` | **0** |
      | `GET /api/v1/audiobooks/soft-deleted` | 3,953 |

      The dedicated endpoint is right, so the data is fine — this is about the list
      endpoint's filter. Note the bare form `?marked_for_deletion=true` returns the full
      63,869; that part is **by design**, not a bug: field filters travel inside the
      `filters` JSON parameter and gin ignores unrecognized bare params. It is worth
      knowing anyway, because `marked_for_deletion` is *not* in `filterFieldQueryParams`
      (`internal/server/handlers/audiobooks/handler.go:259`), so it does not get the
      helpful rejection that `?title=` now gets — it silently returns the whole library.
      Adding it to that set is a candidate fix for half of this; read the comment above
      the set first, it explains why including a name is not always harmless.

      **The `count: 0` is the real question.** Most likely the list path excludes
      soft-deleted rows before the field filter is applied, so filtering *for* them
      inside an already-live-only set can only ever yield zero. If so the honest
      behaviours are either to support it properly or to reject the field — returning 0
      reads as "there are none", which is the wrong answer to a question about 3,953
      books.

      ⚠️ **Unverified: whether this predates PR #2392.** No pre-deploy reading of this
      exact query was taken, and #2392 changed what the memdb `GetAllBooksCore` returns
      by default, so a behaviour change is plausible and must be ruled out rather than
      assumed either way. Check by running the same query against a build from
      `2b017c01` (the commit before the fix landed). Do not file this as a regression
      until that comparison exists.

      Related: [[project_memdb_softdeleted_leak]] for why the default changed, and the
      known-similar case of `version_group_id` being silently ignored on this same
      endpoint.
