### Changed

- Internal cleanup: the small internal "models" package is gone. The dedup score types and their provenance helpers now live with the rest of the dedup scoring code, and the three iTunes import types live in the iTunes package. Stored dedup rows and API payloads keep exactly the same bytes (pinned by new byte-for-byte tests), so nothing changes for users.
