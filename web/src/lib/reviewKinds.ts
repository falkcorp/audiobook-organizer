// file: web/src/lib/reviewKinds.ts
// version: 1.0.0
// guid: 7f3a1c9e-2d84-4b16-9a5f-6c0e8d2b4a71
// last-edited: 2026-07-13

// Human-readable labels for review-queue item Kinds. Single source of truth so
// the queue page, banners, and any future producer UI stay in sync. Kinds not
// listed fall back to a title-cased version of the raw kind string.

export const REVIEW_KIND_LABELS: Record<string, string> = {
  'regroup.multidisc': 'Multi-disc groups',
  'regroup.version-group': 'Abridged / Unabridged editions',
  'regroup.anthology': 'Anthologies / collections',
  'regroup.ambiguous': 'Ambiguous folders',
};

/** labelForKind returns the human label for a review Kind, falling back to a
 *  readable title-cased rendering of the raw kind ("regroup.foo-bar" → "Foo Bar")
 *  so an unknown producer never shows a blank bucket header. */
export function labelForKind(kind: string): string {
  const known = REVIEW_KIND_LABELS[kind];
  if (known) return known;
  const tail = kind.includes('.') ? kind.slice(kind.indexOf('.') + 1) : kind;
  return tail
    .replace(/[-_]/g, ' ')
    .replace(/\b\w/g, (c) => c.toUpperCase());
}
