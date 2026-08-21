<!-- file: changelog.d/20260821_082000_metadata_panel_lift.md -->
<!-- version: 1.0.0 -->
<!-- guid: ea7fae33-c1e9-4103-bd24-bf4b538b0857 -->
<!-- last-edited: 2026-08-21 -->

### Changed

#### The metadata lane owns its own layout, like the other two

`RegroupPanel` and `DupesPanel` were each extracted when their lane was
ported, so `ReviewWorkspace`'s lane branch read as one line for regroup, one
line for dupes, and sixty for metadata. The oldest lane was the only one still
assembled inside the shell, purely because it predated the pattern the later
ports established.

`MetadataPanel` closes that. The shell now owns lane selection and the
cross-lane chrome; each lane owns its layout. No behaviour changes — all 226
review tests pass untouched.

The stale-refetch confirmation deliberately stays in the shell. Refetching every
stale row is thousands of calls to external metadata providers, and the dialog
guarding it is cross-lane chrome sitting next to the rescore dialog. The panel
raises the intent; the shell decides how to ask. A single-row refetch needs no
dialog and is handled in the panel.
