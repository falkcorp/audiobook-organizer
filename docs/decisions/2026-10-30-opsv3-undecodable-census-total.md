<!-- file: docs/decisions/2026-10-30-opsv3-undecodable-census-total.md -->
<!-- version: 1.0.0 -->
<!-- guid: 89f6b71b-a3bf-408a-94fd-6a3ae7e695a0 -->
<!-- last-edited: 2026-10-30 -->

# Ops v3: undecodable run records in the counts census and in Total

## Findings

- `hiddenIDs` (run listing) already set `RunQuery.OnUndecodable`; `hiddenFor` (the `/api/v3/ops/counts` census) did not, so the first corrupt record failed the census with a 500.
- `runsFromIndex` with a nil `Keep` counted index keys for `Total`, so a corrupt record added to Total while the page skipped it.

## Decisions

1. Census: `hiddenFor` sets `OnUndecodable`, logs a warning, and treats the run as hidden, matching `hiddenIDs`. The maintained census (`OpCountsV3`) counts the run under its true state and definition, so the group must give it up or a count would exceed its href's list length. The definition is known from the def index being walked; the state is not (the record is unreadable), so only when a corrupt record exists the handler walks each state index (`state.AllV3()`) and attributes the run to the state that names it. Healthy libraries pay nothing extra.
2. Total: when `OnUndecodable` is set and `Keep` is nil, the store substitutes an accept-all `Keep`, so every record is decoded and an undecodable one is neither counted nor paged. When `OnUndecodable` is nil the key-count fast path is kept: such a caller gets an error wherever the bad record is read, so there is no inconsistent success.

## Cost

Total with a handler now decodes every record under the prefix instead of counting keys. This only affects callers that set `OnUndecodable` with no `Keep` (the store-level contract); the Repairs list always set `Keep`, so its cost is unchanged. A cheaper way to keep the fast path when no corrupt rows exist would need a persisted corrupt-row marker; not worth the write-path complexity.

## Rejected

- Subtracting the corrupt run from the definition count only and leaving state groups off by one: state groups would then exceed their href's list.
- Always decoding for Total, even with no handler: slows non-Repairs def listings for no consistency gain.

## Noted, out of scope

A list filtered to a non-Repairs definition sets no `OnUndecodable`, so a corrupt record in the page still fails that list.
