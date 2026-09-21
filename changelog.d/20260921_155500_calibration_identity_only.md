### Fixed

- Fingerprint workers can now start against a library root the server has not
  fingerprinted yet. The server offered such a root a file that proves only
  that the worker is looking at the same folder, but the worker had no way to
  tell that apart from a useless answer and threw it away — so the root could
  never be proven until it had fingerprints, and could never get fingerprints
  until a worker started. That circle is what stopped all fingerprinting for
  about six hours. The two claims are now distinct, and a worker says plainly
  in its log which roots were accepted on the weaker one. It still refuses to
  run if nothing at all can be checked against the server's own fingerprints.
