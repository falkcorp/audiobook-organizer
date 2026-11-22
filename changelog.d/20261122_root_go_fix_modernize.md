### Changed

- Every `go fix` modernization of the root module's Go code is now applied, across 414 files. The rewrites include:
  - `new(expr)` in place of one-line pointer helpers;
  - `for range n` loops, `sync.WaitGroup.Go` and `errors.AsType`;
  - `slices.Contains`, `maps.Copy` and `strings.SplitSeq`;
  - `reflect.TypeFor`, and similar.

  After these changes, `go fix -diff ./...` at the root prints nothing. `go fix` applies only the edits that do not conflict in one pass, so it was re-run until a pass changed nothing. The 32 pointer helpers it inlined everywhere but left defined, marked `//go:fix inline` with no callers left, are deleted, and staticcheck reports no unused function. CI does not gate this, by design: only `core/` runs `fix-check`.

  The rewrites keep behaviour the same. One source guard noticed a change of shape. The guest-key check "admins get no first phone-app key" is now written as `slices.Contains`, so the cutover's hidden-permission-check pin now sees it. That check tests the target user's roles, not the caller's, so it is listed as an exclusion with that reason.
