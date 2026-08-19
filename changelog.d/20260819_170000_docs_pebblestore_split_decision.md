### Added

#### A costed decision document for the PebbleStore struct split

`docs/plans/2026-08-19-pebblestore-struct-split-decision.md` supplies the
per-method evidence the surface-split plan said it lacked, so the question can be
answered yes or no rather than left open.

Per-method analysis of all 558 `*PebbleStore` methods: 420 touch core fields
only, 118 touch no struct fields at all, and just **20 (3.6%)** touch any
domain-local field. `db` alone is touched by 407.

This confirms the original "N facades over one object" objection while inverting
what it means. It was offered as evidence the state is dangerously entangled; the
measurement shows the domains have almost no state to entangle. There are no
lock-ordering hazards to design around — so the split is mechanically low-risk
and high-churn, and it decomposes a method set rather than decoupling state.

The document does **not** recommend proceeding. It makes the trade visible, names
the only 20 methods needing thought, orders the work so all structural risk lands
in a single reviewable first PR, and records that its own numbers have not been
independently reproduced and should be re-derived before execution.
