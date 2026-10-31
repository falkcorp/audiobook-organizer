<!-- file: docs/decisions/2026-10-30-ops-dirtyset-compare-and-delete.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6a4e1b97-3d28-4c5f-8e70-b91f2c0d7a46 -->
<!-- last-edited: 2026-10-30 -->

# OPS-DIRTYSET-CAS: compare-and-delete for the v3 DirtySet source

## Decision log

Context: the v3 Batch runner's `ops.DirtySet` reader (05-PR5) pages a key
family with `ScanPrefixPage` and clears each processed entry with
`CompareAndDeleteRaw`. `*PebbleStore` already had `ScanPrefixPage`, and
`MarkIdentDirty` / `ClearIdentDirty` (02-PR4) already implemented the same
epoch protocol for `ident:dirty:`. Only the generic primitive was missing, so
a DirtySet run was refused with `ErrDirtySetUnsupported`.

Chosen:

- New file `internal/database/dirty_set.go` with `CompareAndDeleteRaw(key,
  expect)` and the generic marker `MarkDirty(prefix, subjectID)`.
- Atomicity uses the store's existing striped mutexes (`metadataCacheLock`),
  not a Pebble indexed batch. The neighbouring raw helpers and the identification
  marker serialize on those stripes, and a Pebble batch alone cannot make
  read-compare-delete atomic against a concurrent plain `Set`.
- The lock for an `ident:dirty:<id>` key is the book's stripe (id), exactly as
  before; any other key is striped by its whole key. Marker and delete derive
  the lock from the key alone, so they always agree, and no new field is added
  to `PebbleStore`.
- `MarkIdentDirty` and `ClearIdentDirty` now delegate to the generic pair, so
  there is one implementation of the protocol and the existing identification
  tests exercise it. Epochs come from the existing `nextIdentEpoch`.
- The comparison is the full stored value (`bytes.Equal`), matching what the
  runner passes (the value it read). Missing key or different value returns
  `(false, nil)`.
- Keys in the `opv2:op:` family and `metadata_cache:` rows are rejected: both
  carry derived indexes that only their own writers maintain.
- Capability detection needed no change: the registry already looks the store
  up with `database.AsCapability[dirtySetStore]`, which now succeeds for
  `*PebbleStore` (asserted in the new registry test).

Rejected:

- Removing `ErrDirtySetUnsupported`: it is still the correct outcome for any
  store lacking the primitive (the mock and non-Pebble stores), so the refusal
  is reachable and stays.
- A Pebble indexed batch for the compare-and-delete: see above.
- A separate lock array for generic families: more state for no gain, since
  contention between unrelated dirty entries on a 64-way stripe is negligible.
- Wiring producers (scanner, merge) to mark the identification family: that is
  02-PR14's driver work, outside this primitive.
