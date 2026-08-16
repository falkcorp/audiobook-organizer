### Changed

- The server now refuses to start if any background operation failed to
  register, instead of logging a warning and carrying on. An operation that
  fails to register doesn't degrade — it ceases to exist, while everything else
  reports healthy, so the failure only surfaces later as an unrecognised
  operation on a server that looks fine. Refusing to start is the louder and
  much cheaper failure.
