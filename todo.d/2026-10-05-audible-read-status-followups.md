- [ ] **ARS-UI** Repairs tab: add an upload control for the
      `maintenance.audible-read-status` fixer. The Plan button sends no params,
      and this fixer needs `target_user_id` and the export's `items`. Today the
      plan has to be started with `POST /api/v1/repairs/maintenance.audible-read-status/plan`;
      after that, the tab shows the plan and can approve and apply rows. The
      control needs a user picker that never defaults to the caller, plus a
      JSON file input that strips each item down to the fields the fixer reads,
      so the body stays under the JSON body limit.
- [ ] **ARS-EXTID** `maintenance.audible-read-status` matches ASINs only
      against the book-level `asin` field. An ASIN stored only in external ids
      falls through to the title tier or to unmatched. Consider indexing the
      external-id ASINs as well.
