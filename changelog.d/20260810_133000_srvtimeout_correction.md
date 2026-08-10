<!--
Documentation-only: corrects TODO-SRVTIMEOUT and the Makefile coverage comment
with the 2026-08-10 measurement showing internal/server's ~500s runtime is a
macOS temp-filesystem cost (532s -> 33.7s with TMPDIR on a RAM disk, vs 35.5s
on Linux), not the package being slow. No behaviour change, so this fragment is
intentionally a no-op.
-->
