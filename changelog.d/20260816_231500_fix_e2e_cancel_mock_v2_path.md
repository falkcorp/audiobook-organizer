- Fixed the end-to-end test double for cancelling an operation, which still
  answered on the old address after the real cancel moved to a new one. No
  effect on the application itself — the cancel button was working; the test
  harness was not following it.
