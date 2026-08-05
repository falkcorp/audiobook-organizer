<!-- file: changelog.d/20260805_040000_regroup_folderref_label.md -->
<!-- version: 1.0.0 -->
<!-- guid: d721dc75-fe57-443d-9b94-7ffd831a58c3 -->
<!-- last-edited: 2026-08-05 -->

### Fixed

- **Regroup holds could be labelled with the AUTHOR folder instead of the book.** A
  production group of 17 tracks — every one of them
  `Rysa Walker - The Delphi Effect ... (Unabridged)` — was shown as
  `/abooks/imported/Rysa Walker`.

  The grouping was correct; only the label was wrong. The parent directory carried an
  edition marker, so `folderKeyOf` took the edition branch and returned the
  *grandparent* — which is the book for `<Book>/<Book> (Unabridged)/files`, but the
  author for `<Author>/<Book> (Unabridged)/files`.

  🔑 This is a correctness problem, not a cosmetic one. With ~900 holds to work
  through, a reviewer reading "Rysa Walker" would reasonably reject it as an
  author-folder merge and discard a *good* regroup — the same mistrust that made this
  queue feel unsafe in the first place.

  The displayed folder is now the members' shared directory when they all sit in one,
  and the grandparent only when they genuinely span sibling shells (the real shatter
  shape, where naming one chapter dir would be worse). **Display only — the grouping
  key is untouched, so which books belong together does not change.**

  3 tests: the production shape, the spanning shape that must keep the grandparent,
  and the helper's fallbacks.
