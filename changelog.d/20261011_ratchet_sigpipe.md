### Fixed

- **The errcheck and interface-width ratchets no longer fail at random.** Under
  `set -o pipefail`, `printf "$output" | grep -q PATTERN` let `grep -q` exit at its
  first match and SIGPIPE the `printf`, so the pipeline reported failure on a
  match: the "did it reach its summary line" check then exited 2 on a good run
  (seen on 2026-10-11, alternating rc 0 / rc 2 on the same tree), and the
  typecheck guard could miss a real typecheck failure. Both scripts now feed
  `grep -q` a here-string.
