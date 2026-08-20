<!-- file: changelog.d/20260820_180000_fetch_op_toast_once.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6d4b9a02-8f1c-4e57-93a0-5b2c7e8d1f43 -->
<!-- last-edited: 2026-08-20 -->

### Fixed

#### A failed metadata fetch no longer stacks a wall of identical toasts

The library page watches the operations store for the metadata fetch it started,
but never forgot the operation once it finished. The store rebuilds its
operation array from scratch on every poll, so the watcher re-ran on every tick
and re-fired its result. Error toasts do not auto-dismiss, so a failed fetch
piled up identical, undismissable "Metadata fetch failed." toasts until it hit
the notification cap. Success had the same fault, bounded only by navigating
away.
