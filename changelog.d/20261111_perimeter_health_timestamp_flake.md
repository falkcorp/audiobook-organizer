### Fixed

- The perimeter test through the running app no longer fails intermittently when its two `/health` answers straddle a second boundary: the comparison removes the per-request `timestamp` field from both bodies and still requires everything else to match.
