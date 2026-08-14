// file: web/src/components/audiobooks/fieldRangeSelect.ts
// version: 1.0.0
// guid: 6f2c8a41-9d57-4e03-b1a6-4c7e2d9f5b18
// last-edited: 2026-08-14

/**
 * applyFieldClick implements file-manager selection semantics for the
 * metadata matcher's field checkboxes: a plain click toggles one field and
 * becomes the anchor; a shift-click SELECTS (never deselects) every visible
 * field between the anchor and the clicked field, inclusive, and moves the
 * anchor. Fields hidden for this candidate (no value) are not part of the
 * range because they are not in visibleFields.
 */
export function applyFieldClick(
  prev: Set<string>,
  field: string,
  shiftKey: boolean,
  anchor: string | null,
  visibleFields: string[],
): { next: Set<string>; anchor: string } {
  const next = new Set(prev);
  const ai = anchor ? visibleFields.indexOf(anchor) : -1;
  const fi = visibleFields.indexOf(field);
  if (shiftKey && ai >= 0 && fi >= 0) {
    const [lo, hi] = ai < fi ? [ai, fi] : [fi, ai];
    for (let i = lo; i <= hi; i++) next.add(visibleFields[i]);
  } else if (next.has(field)) {
    next.delete(field);
  } else {
    next.add(field);
  }
  return { next, anchor: field };
}
