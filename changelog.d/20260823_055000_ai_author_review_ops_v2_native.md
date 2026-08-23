### Fixed

- The AI duplicate-author review and its apply step now report the operation
  that actually runs. Both used to hand back the id of a bookkeeping row that
  no longer had a status route behind it, so progress for a review that was
  running fine could not be looked up at all.

- A completed operation's result is now readable whichever way the operation
  stored it. `GET /operations/:id/result` only knew the older of the two
  places results are kept, so an operation that had been moved to the newer
  one wrote its output and then had no way to hand it back — the AI review's
  suggestions were about to land in exactly that gap.

- Starting a review while one of the same kind is already running still hands
  back the run in flight rather than starting a second one, and a review of
  the *other* kind is no longer blocked by it.
