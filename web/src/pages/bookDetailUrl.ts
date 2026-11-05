// file: web/src/pages/bookDetailUrl.ts
// version: 1.0.0
// guid: 2c8e5a71-b3d0-4f96-8a14-d7e90c6b3f52
// last-edited: 2026-11-05
//
// What the book page accepts from its address bar. `?compare=` reaches the
// server as `snapshot_ts`, which it parses as RFC3339 and answers 400 to
// anything else, so a value that is not one is never used (and is removed from
// the address bar by the page).

const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;

/** `raw` when it is an RFC3339 timestamp that is a real instant, else null. */
export function parseSnapshotTs(raw: string | null | undefined): string | null {
  if (!raw || !RFC3339.test(raw)) return null;
  return Number.isNaN(Date.parse(raw)) ? null : raw;
}
