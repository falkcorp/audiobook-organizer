### Added

- **launchd agent for the Whisper reverse SSH tunnel.** The Metal worker binds
  `127.0.0.1` only and is published to the server through
  `ssh -N -R 19848:127.0.0.1:19848`, so the server reaches it on its own loopback
  and nothing is exposed on the LAN. The agent passes `ControlMaster=no` and
  `ControlPath=none` deliberately: with a shared SSH control master configured,
  `ssh -R` attaches to an interactive session's connection, registers the forward
  there and exits 0, producing a tunnel that works until that unrelated session
  ends — and that `KeepAlive` cannot fix, because the exit looks clean.
