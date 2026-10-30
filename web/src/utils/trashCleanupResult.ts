// file: web/src/utils/trashCleanupResult.ts
// version: 1.5.0
// guid: dfb540be-d220-4087-9ed6-56cdd16b0e8c
// last-edited: 2026-10-29

/** The def id of the nightly trash cleanup operation. */
export const TRASH_CLEANUP_DEF_ID = 'maintenance.trash-cleanup';

/**
 * Reports whether an operation is a run of the trash cleanup. Only the current
 * id counts: the panel it opens reads the kept / needs-attention lists the
 * maintenance def stores, which a run under its former id (a stored row keeps
 * the id it was written with) never had, and the op-ID alias guard forbids
 * code naming a former id.
 */
export function isTrashCleanupDef(defId: string | undefined | null): boolean {
  return defId === TRASH_CLEANUP_DEF_ID;
}

/**
 * One file of the kept-file sweep's report (maintenance.KeptSweepFile): the
 * version and book that noted it, and a reason. For the would-remove and
 * would-drop lists that is why the purge had kept the file; for the escalated
 * list, why the note could not be settled.
 */
export interface KeptSweepFile {
  version_id: string;
  book_id: string;
  path: string;
  reason: string;
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
  /** versions left in the trash because the book serves nothing that exists on disk (its path is stale or lost, or a folder with no file in it); nothing was deleted, not counted in failed. The file paths are the books' own paths, the thing to fix */
  nothing_served: number;
  nothing_served_version_ids: string[];
  nothing_served_book_ids: string[];
  nothing_served_file_paths: string[];
  /** versions kept in the trash because their book still plays one of their files (restore the version or change what the book plays); nothing was deleted, not counted in failed */
  served_kept: number;
  served_kept_version_ids: string[];
  served_kept_book_ids: string[];
  served_kept_file_paths: string[];
  /** versions a swap left in swapping_out / swapping_in: their book has no active version until the swap is resumed (Version Management, Unfinished swaps). The run only lists them and moves nothing; not counted in failed. The file paths are the books' own paths */
  swap_incomplete: number;
  swap_incomplete_version_ids: string[];
  swap_incomplete_book_ids: string[];
  swap_incomplete_file_paths: string[];
  /**
   * The kept-file sweep revisits files earlier purges left on disk. It ships
   * report-only (kept_sweep_apply_enabled off): it then lists what it would
   * remove and removes nothing, so removed and gone are zero.
   */
  kept_sweep_report_only: boolean;
  /** files a report-only run found nothing naming any more (the files it would unlink once switched on) */
  kept_sweep_would_remove: number;
  /** notes of files already gone that a report-only run would drop */
  kept_sweep_would_drop: number;
  kept_sweep_would_remove_files: KeptSweepFile[];
  kept_sweep_would_drop_files: KeptSweepFile[];
  /** files the sweep unlinked (nothing names them any more) */
  kept_sweep_removed: number;
  /** notes of already-gone files the sweep dropped */
  kept_sweep_gone: number;
  /** files left in place because something still owns them */
  kept_sweep_still_kept: number;
  /** files left in place because the sweep could not decide or act; tried again next run */
  kept_sweep_kept_on_error: number;
  /** versions the sweep moved to purge_failed this run: a note stayed unsettled for several nights in a row */
  kept_sweep_escalated: number;
  kept_sweep_escalated_version_ids: string[];
  kept_sweep_escalated_files: KeptSweepFile[];
  /** a merge or combine held the merge lock for the whole wait, so the run stopped and left the rest for the next night */
  stopped_for_merge: boolean;
  /** how many nights in a row, this one included, the run stopped for the merge lock */
  stopped_for_merge_nights: number;
  /** the run has stopped for the merge lock several nights in a row (a warning, not a failure) */
  stopped_for_merge_repeatedly: boolean;
}

/** The groups of versions the result points at. */
export type TrashCleanupSet =
  'kept' | 'needs_attention' | 'cannot_tell' | 'nothing_served' | 'served_kept' | 'swap_incomplete';

const num = (v: unknown): number => (typeof v === 'number' && Number.isFinite(v) ? v : 0);
const strings = (v: unknown): string[] =>
  Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];

const sweepFiles = (v: unknown): KeptSweepFile[] =>
  Array.isArray(v)
    ? v
        .filter((x): x is Record<string, unknown> => !!x && typeof x === 'object')
        .map((x) => ({
          version_id: typeof x.version_id === 'string' ? x.version_id : '',
          book_id: typeof x.book_id === 'string' ? x.book_id : '',
          path: typeof x.path === 'string' ? x.path : '',
          reason: typeof x.reason === 'string' ? x.reason : '',
        }))
    : [];

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
    nothing_served: num(r.nothing_served),
    nothing_served_version_ids: strings(r.nothing_served_version_ids),
    nothing_served_book_ids: strings(r.nothing_served_book_ids),
    nothing_served_file_paths: strings(r.nothing_served_file_paths),
    served_kept: num(r.served_kept),
    served_kept_version_ids: strings(r.served_kept_version_ids),
    served_kept_book_ids: strings(r.served_kept_book_ids),
    served_kept_file_paths: strings(r.served_kept_file_paths),
    swap_incomplete: num(r.swap_incomplete),
    swap_incomplete_version_ids: strings(r.swap_incomplete_version_ids),
    swap_incomplete_book_ids: strings(r.swap_incomplete_book_ids),
    swap_incomplete_file_paths: strings(r.swap_incomplete_file_paths),
    kept_sweep_report_only: r.kept_sweep_report_only === true,
    kept_sweep_would_remove: num(r.kept_sweep_would_remove),
    kept_sweep_would_drop: num(r.kept_sweep_would_drop),
    kept_sweep_would_remove_files: sweepFiles(r.kept_sweep_would_remove_files),
    kept_sweep_would_drop_files: sweepFiles(r.kept_sweep_would_drop_files),
    kept_sweep_removed: num(r.kept_sweep_removed),
    kept_sweep_gone: num(r.kept_sweep_gone),
    kept_sweep_still_kept: num(r.kept_sweep_still_kept),
    kept_sweep_kept_on_error: num(r.kept_sweep_kept_on_error),
    kept_sweep_escalated: num(r.kept_sweep_escalated),
    kept_sweep_escalated_version_ids: strings(r.kept_sweep_escalated_version_ids),
    kept_sweep_escalated_files: sweepFiles(r.kept_sweep_escalated_files),
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
    case 'nothing_served':
      return r.nothing_served_version_ids;
    case 'served_kept':
      return r.served_kept_version_ids;
    case 'swap_incomplete':
      return r.swap_incomplete_version_ids;
  }
}

/** The distinct book ids behind one of the result's counts. */
export function cleanupBookIds(r: TrashCleanupResult, set: TrashCleanupSet): string[] {
  const ids = {
    kept: r.kept_book_ids,
    needs_attention: r.needs_attention_book_ids,
    cannot_tell: r.cannot_tell_book_ids,
    nothing_served: r.nothing_served_book_ids,
    served_kept: r.served_kept_book_ids,
    swap_incomplete: r.swap_incomplete_book_ids,
  }[set];
  return [...new Set(ids)];
}

/**
 * Where a count of the result links to: the Version Management page, narrowed
 * to the versions of that run's group. The page reads the ids from the
 * operation's stored result (op), so the link stays short however many
 * versions the run named. Kept versions have finished purging (Purged tab);
 * versions that need attention, those the cleanup could not judge, those whose
 * book serves nothing on disk and those a book still plays are still listed in
 * the Trash tab; the versions of an unfinished swap are in no trash at all, and
 * their books are listed in the Unfinished swaps tab.
 */
export function cleanupVersionsLink(opId: string, set: TrashCleanupSet): string {
  return `/versions?op=${encodeURIComponent(opId)}&set=${set}`;
}

/** Which Version Management tab lists the versions of a group. */
export function cleanupSetTab(set: TrashCleanupSet): 'trash' | 'purged' | 'swaps' {
  if (set === 'kept') return 'purged';
  return set === 'swap_incomplete' ? 'swaps' : 'trash';
}

/** Reads the set query value back; null for anything but the known sets. */
export function parseCleanupSet(v: string | null): TrashCleanupSet | null {
  return v === 'kept' ||
    v === 'needs_attention' ||
    v === 'cannot_tell' ||
    v === 'nothing_served' ||
    v === 'served_kept' ||
    v === 'swap_incomplete'
    ? v
    : null;
}
