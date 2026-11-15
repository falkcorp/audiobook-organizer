### Fixed

- core: the diagnostics bundle names keys that redact to the same text in linear time. Sixteen thousand path keys took 12.8 s (a 100,000-file section would have outlasted the 30-minute export task); the names are unchanged ("<path>", "<path>#2", "<path>#3", in the sorted order of the original keys).

### Changed

- core: the diagnostics routes stay unmounted until a replacement for the path redactor lands (F8-03b). `diagnostics.RedesignPendingF8_03b` is true, and `archtest.F803bGate` fails the build if non-test code imports `core/internal/healthhttp` or builds the bundle exporter while it is. The review of F8-03 found that the current redactor still lets usernames and directory trees through for names with spaces, `~user`, `smb://` paths and build flags, and that it cannot be made safe by more rules; the seams note (`docs/agent-tasks/platform-core/seams.d/F8-03.md`) lists the open cases and corrects its earlier claims about an admin's unredacted view: there is none, every bundle is redacted the same way.
