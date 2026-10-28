// file: web/src/utils/trashCleanupResult.ts
// version: 1.2.0
// guid: dfb540be-d220-4087-9ed6-56cdd16b0e8c
// last-edited: 2026-10-28

/** The def id of the nightly trash cleanup operation. */
export const TRASH_CLEANUP_DEF_ID = 'maintenance.trash-cleanup';

/** The def id it ran under before the maintenance plugin took it over. */
const FORMER_TRASH_CLEANUP_DEF_ID = 'scheduler.trash-cleanup';

/** Reports whether an operation is a run of the trash cleanup (under either id). */
export function isTrashCleanupDef(defId: string | undefined | null): boolean {
  return defId === TRASH_CLEANUP_DEF_ID || defId === FORMER_TRASH_CLEANUP_DEF_ID;
}

/**
 * The result the trash cleanup stores on its operation
 * (maintenance.TrashCleanupResult). The id lists are what the counts link to.
 */
export interface TrashCleanupResult {
  purged: number;
  failed: number;
  /** versions purged with files left on disk because another row or book names them (also counted in purged) */
  kept_files: number;
  kept_version_ids: string[];
  kept_book_ids: string[];
  kept_file_paths: string[];
  /** versions whose purge gave up on a file it cannot remove (purge_failed); not counted in failed */
  needs_attention: number;
  needs_attention_version_ids: string[];
  needs_attention_book_ids: string[];
  /** versions left alone because the purge could not tell whether their files are gone (a drive that is down looks the same); still in the trash, not counted in failed */
  cannot_tell: number;
  cannot_tell_version_ids: string[];
  cannot_tell_book_ids: string[];
  /** versions kept in the trash because their book still plays one of their files (restore the version or change what the book plays); nothing was deleted, not counted in failed */
  served_kept: number;
  served_kept_version_ids: string[];
  served_kept_book_ids: string[];
  served_kept_file_paths: string[];
  /** a merge or combine held the merge lock for the whole wait, so the run stopped and left the rest for the next night */
  stopped_for_merge: boolean;
  /** how many nights in a row, this one included, the run stopped for the merge lock */
  stopped_for_merge_nights: number;
  /** the run has stopped for the merge lock several nights in a row (a warning, not a failure) */
  stopped_for_merge_repeatedly: boolean;
}

/** The groups of versions the result points at. */
export type TrashCleanupSet = 'kept' | 'needs_attention' | 'cannot_tell' | 'served_kept';

const num = (v: unknown): number => (typeof v === 'number' && Number.isFinite(v) ? v : 0);
const strings = (v: unknown): string[] =>
  Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];

/**
 * Reads a stored operation result as a trash cleanup result. The server omits
 * empty id lists and a false stopped_for_merge, so every field has a default.
 * null when the value is not an object (no result stored, or text).
 */
export function parseTrashCleanupResult(raw: unknown): TrashCleanupResult | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  const r = raw as Record<string, unknown>;
  return {
    purged: num(r.purged),
    failed: num(r.failed),
    kept_files: num(r.kept_files),
    kept_version_ids: strings(r.kept_version_ids),
    kept_book_ids: strings(r.kept_book_ids),
    kept_file_paths: strings(r.kept_file_paths),
    needs_attention: num(r.needs_attention),
    needs_attention_version_ids: strings(r.needs_attention_version_ids),
    needs_attention_book_ids: strings(r.needs_attention_book_ids),
    cannot_tell: num(r.cannot_tell),
    cannot_tell_version_ids: strings(r.cannot_tell_version_ids),
    cannot_tell_book_ids: strings(r.cannot_tell_book_ids),
    served_kept: num(r.served_kept),
    served_kept_version_ids: strings(r.served_kept_version_ids),
    served_kept_book_ids: strings(r.served_kept_book_ids),
    served_kept_file_paths: strings(r.served_kept_file_paths),
    stopped_for_merge: r.stopped_for_merge === true,
    stopped_for_merge_nights: num(r.stopped_for_merge_nights),
    stopped_for_merge_repeatedly: r.stopped_for_merge_repeatedly === true,
  };
}

/** The version ids behind one of the result's counts. */
export function cleanupVersionIds(r: TrashCleanupResult, set: TrashCleanupSet): string[] {
  switch (set) {
    case 'kept':
      return r.kept_version_ids;
    case 'needs_attention':
      return r.needs_attention_version_ids;
    case 'cannot_tell':
      return r.cannot_tell_version_ids;
    case 'served_kept':
      return r.served_kept_version_ids;
  }
}

/** The distinct book ids behind one of the result's counts. */
export function cleanupBookIds(r: TrashCleanupResult, set: TrashCleanupSet): string[] {
  const ids = {
    kept: r.kept_book_ids,
    needs_attention: r.needs_attention_book_ids,
    cannot_tell: r.cannot_tell_book_ids,
    served_kept: r.served_kept_book_ids,
  }[set];
  return [...new Set(ids)];
}

/**
 * Where a count of the result links to: the Version Management page, narrowed
 * to the versions of that run's group. The page reads the ids from the
 * operation's stored result (op), so the link stays short however many
 * versions the run named. Kept versions have finished purging (Purged tab);
 * versions that need attention, those the cleanup could not judge and those
 * a book still plays are still listed in the Trash tab.
 */
export function cleanupVersionsLink(opId: string, set: TrashCleanupSet): string {
  return `/versions?op=${encodeURIComponent(opId)}&set=${set}`;
}

/** Which Version Management tab lists the versions of a group. */
export function cleanupSetTab(set: TrashCleanupSet): 'trash' | 'purged' {
  return set === 'kept' ? 'purged' : 'trash';
}

/** Reads the set query value back; null for anything but the known sets. */
export function parseCleanupSet(v: string | null): TrashCleanupSet | null {
  return v === 'kept' || v === 'needs_attention' || v === 'cannot_tell' || v === 'served_kept'
    ? v
    : null;
}
