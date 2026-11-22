### Changed

- The shell's client now reads core's generated route types and operation table for the sign-in, session and profile routes instead of a hand-typed copy; the settings routes keep their refined shapes until core.json types them. Nothing in the running web app imports this path yet.
- The shell's user menu saves a profile email and a password through core's own routes when the host does not supply its own callbacks.
