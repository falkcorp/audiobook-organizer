- [ ] **INVITE-PATH-REDACTION-FOLLOWUPS** Invite-path redaction follow-ups (#4112):
      (1) `internal/server/middleware/log_path_guard_test.go` only sees a
      `<x>.URL.Path` that is a direct argument of a log call: it misses a path
      built with `+` (a `BinaryExpr`) or put in a local first, and it accepts any
      call whose name contains `Redact`, including `RedactQueryCredentials`, which
      does not touch the path. Make it resolve locals and require the invite
      redactor (`logging.RedactInvitePath` or `cfLogPath`) by name;
      (2) the allocation-linearity test (`TestRedactInvitePath_LinearAllocs`) only
      exercises `anyInvitePrefix`; add inputs that put the same pressure on the
      cleaning, decoding and anchored-match stages, and note that a truly
      quadratic version fails only at the `go test` timeout because
      `AllocsPerRun` cannot be interrupted;
      (3) the remaining raw `URL.Path` log or audit sites on non-invite surfaces
      (`absauthprobe.go`, `handlers/activity.go`, `handlers/abs/stream.go`, the
      audit `Path` fields in `handlers/abs/login.go` and `absauth.go`) are
      allowlisted or unchecked: route them through one path-for-log helper.
