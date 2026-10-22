// file: web/src/utils/trackedOperations.ts
// version: 1.0.0
// guid: 7e3b9a41-5c06-4d28-b1f7-2a8d0c6e4f95
// last-edited: 2026-10-22

/**
 * The background operations (merge, combine, purge) the page is following, in a
 * form that survives a reload. The in-flight set used to live in React state of
 * the Library page, so leaving the page (or reloading) dropped it: the outcome
 * toast never came and the books were free to be submitted again while the
 * server was still working on them. The app-level tracker keeps the set and
 * mirrors it here (sessionStorage, so it follows the tab and not the browser).
 */

export type TrackedKind = 'merge' | 'combine' | 'purge';

export interface TrackedOpDescriptor {
  operationId: string;
  kind: TrackedKind;
  /** The books the operation covers; they stay locked until it ends. Empty for a purge. */
  bookIds: string[];
  /** book id -> title, for the merge outcome wording */
  titles?: Record<string, string>;
  /** how many books a merge was asked to fold in */
  requested?: number;
  /** ms since the epoch when tracking began */
  startedAt: number;
}

export const TRACKED_OPS_STORAGE_KEY = 'aorg.trackedOperations.v1';

/** An entry older than this is dropped on load: the operation is long over and its record may be pruned. */
export const TRACKED_OP_MAX_AGE_MS = 24 * 60 * 60 * 1000;

const KINDS: readonly string[] = ['merge', 'combine', 'purge'];

function isDescriptor(v: unknown): v is TrackedOpDescriptor {
  if (!v || typeof v !== 'object') return false;
  const d = v as Partial<TrackedOpDescriptor>;
  return (
    typeof d.operationId === 'string' &&
    d.operationId !== '' &&
    typeof d.kind === 'string' &&
    KINDS.includes(d.kind) &&
    Array.isArray(d.bookIds) &&
    d.bookIds.every((b) => typeof b === 'string') &&
    typeof d.startedAt === 'number'
  );
}

function store(): Storage | null {
  try {
    return typeof sessionStorage === 'undefined' ? null : sessionStorage;
  } catch {
    return null;
  }
}

/** Reads the persisted set; anything unreadable, malformed or stale is dropped. */
export function loadTrackedOps(now: number = Date.now()): TrackedOpDescriptor[] {
  try {
    const raw = store()?.getItem(TRACKED_OPS_STORAGE_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    const seen = new Set<string>();
    const out: TrackedOpDescriptor[] = [];
    for (const v of parsed) {
      if (!isDescriptor(v) || now - v.startedAt > TRACKED_OP_MAX_AGE_MS) continue;
      if (seen.has(v.operationId)) continue;
      seen.add(v.operationId);
      out.push(v);
    }
    return out;
  } catch {
    return [];
  }
}

/** Writes the set. Storage being unavailable or full only costs the resume after a reload. */
export function saveTrackedOps(list: readonly TrackedOpDescriptor[]): void {
  try {
    const s = store();
    if (!s) return;
    if (list.length === 0) s.removeItem(TRACKED_OPS_STORAGE_KEY);
    else s.setItem(TRACKED_OPS_STORAGE_KEY, JSON.stringify(list));
  } catch {
    // best effort
  }
}
