// file: web/src/modules/books/layout/taskKinds.ts
// version: 0.1.0
// guid: 5d9b3e72-0c48-4f16-a7e5-b2c6d1f08a94
// last-edited: 2026-11-21

// The books `taskKinds` contribution (07 §7.4.2): the display label of each task
// kind the operations bell and the Activity page name, keyed by the core kind a
// run carries after the R2 tasks cutover. The labels are the ones
// components/layout/operationsFormat.ts holds today for the legacy def ids.
//
// Each entry names its legacy def id AND its kind. The kind is what the shell
// looks up; the def id is only the evidence for it, and taskKinds.core.test.ts
// resolves every def id through the kind-rename table
// (internal/books/cutover/kindrename: rules.go and the generated map_gen.go) and
// fails when the kind written here is not where the table sends that id. Nothing
// here is typed from memory of the table.
//
// Not covered, because no legacy def id matches them by name and the table
// cannot say which kind they meant: the legacy label keys `metadata_fetch`,
// `ol_dump_import`, `dedup-scan` and `dedup-acoustid-scan`. They stay in
// operationsFormat.ts until someone who knows which def each one labelled adds
// them here with a def id the test can resolve.

import type { TaskKindPresentation } from '@mediacore/shell';

/** One labelled kind and the legacy def id it replaces. */
export interface BooksKindLabel {
  /** HEAD's operation definition ID (the key of kindrename's defTargets). */
  readonly defId: string;
  /** The kind core records runs under after the cutover. */
  readonly kind: `books.${string}`;
  readonly label: string;
  /**
   * The def id maps to books.retired.<id> (rule 5): the kind is never registered,
   * only old runs carry it, so the label is for history rows.
   */
  readonly history?: true;
  /**
   * Another kind this def id's runs can land in, with the same label: a
   * library.scan run becomes books.library.scan-full when its params are the full
   * sweep's (kindrename MarkerLibraryScan).
   */
  readonly alsoKind?: `books.${string}`;
}

export const BOOKS_KIND_LABELS: readonly BooksKindLabel[] = [
  { defId: 'itunes.import', kind: 'books.itunes.import', label: 'iTunes Import' },
  { defId: 'itunes.sync', kind: 'books.retired.itunes.sync', label: 'iTunes Sync', history: true },
  {
    defId: 'library.scan',
    kind: 'books.library.scan',
    label: 'Library Scan',
    alsoKind: 'books.library.scan-full',
  },
  { defId: 'library.organize', kind: 'books.library.organize', label: 'Organize' },
  { defId: 'metadata.candidate-fetch', kind: 'books.metadata.candidate-fetch', label: 'Metadata Fetch (Batch)' },
  { defId: 'dedup.llm-review', kind: 'books.dedup.llm-review', label: 'Dedup AI Review' },
  { defId: 'dedup.book-signature-scan', kind: 'books.dedup.book-signature-scan', label: 'Book Signature Scan' },
  { defId: 'dedup.embed-scan', kind: 'books.dedup.embed-scan', label: 'Embedding Rescan' },
  { defId: 'acoustid.fingerprint-rescan', kind: 'books.acoustid.fingerprint-rescan', label: 'Fingerprint Rescan' },
  {
    defId: 'activity.sql-migration',
    kind: 'books.retired.activity.sql-migration',
    label: 'Activity Log Migration',
    history: true,
  },
];

type BooksTaskKinds = Readonly<Record<`books.${string}`, TaskKindPresentation>>;

function build(): BooksTaskKinds {
  const out: Record<`books.${string}`, TaskKindPresentation> = {};
  for (const e of BOOKS_KIND_LABELS) {
    out[e.kind] = { label: e.label };
    if (e.alsoKind) out[e.alsoKind] = { label: e.label };
  }
  return out;
}

/** The books module's `taskKinds` (ModuleUIInput.taskKinds). */
export const booksTaskKinds: BooksTaskKinds = build();
