### Interface width: the five the sweep did not reach

The flat-list split sweep took `interfacebloat` from 28 findings to 5 (#2542,
#2545, #2546, #2547, #2549, #2550, #2553, #2554, #2556). The five survivors are
recorded in
[`docs/audits/2026-08-18-interface-width-shapes.md`](docs/audits/2026-08-18-interface-width-shapes.md)
§6 and are **phase-2 work, not leftovers** — none of them yields to
split-then-compose:

- [ ] `database.Store` (40) — make unreachable rather than smaller (plan phase 2).
- [ ] `itunes/service.Store` (17 declared / 24 called) — 7 assignability
      constraints incl. `database.OperationStore`; needs the parameter-type fix
      #2552 applied to its helpers first.
- [ ] `maintenance.JobStore` (12) — deliberate choice from the #2534 arbitration;
      revisit only as per-job interfaces (plan phase 2, item 1).
- [ ] `audiobookStore` / `audiobookUpdateStore` (11 each) — the service calls **44
      distinct store methods**. The finding is that the *service* is too big; do
      not re-group the interface in place, it scores worse on the gate and reads
      no better.

Gate state: the width ratchet (#2548) pins the baseline at 5, so these cannot
grow silently, and a PR adding a sixth has to justify it or add a `//nolint:
interfacebloat` with a reason.
