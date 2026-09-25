### Changed

- ABS `PATCH /api/me/progress/:id` logs the fields the client sent and, when the stored record wins the merge, both sides of that decision, so a "marked finished but it didn't stick" report can be diagnosed from the log.
