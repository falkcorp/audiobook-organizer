Fixed: undoing a retired-versions Apply could put a retired shell's external id
and merged-into link back while its files stayed retired under another book (for
example after a later Apply re-homed the retired version), leaving a book that
looked live and owned nothing. The shell's id and link rows are now reverted last,
and only when every retire row of that shell was restored; otherwise they are
refused with "dependent change not reverted" and the preflight offers no Undo for
them. The retired-versions fixer also holds (retry later, nothing written) a shell
that has a version stuck in a half-finished swap. Corrected two stale comments
about the retired-shell delete bypass.
