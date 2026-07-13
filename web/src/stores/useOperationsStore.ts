// file: web/src/stores/useOperationsStore.ts
// version: 3.6.2
// guid: 2a3b4c5d-6e7f-8a9b-0c1d-2e3f4a5b6c7d
// last-edited: 2026-07-13

import { create } from 'zustand';
import * as api from '../services/api';
import { type OperationSSEEventName } from '../services/api';
import { useAppStore } from './useAppStore';

export interface ActiveOperation {
  id: string;
  /** def_id is the canonical operation identifier, e.g.
   *  "scheduler.purge-deleted". Use this for equality checks like
   *  `op.def_id === 'scan'`. Kept separate from the legacy `type`
   *  field so existing call sites that match on the old short name
   *  ('scan', 'itunes_import', 'ol_dump_import') keep working. */
  def_id?: string;
  /** Plugin namespace, e.g. "scheduler", "maintenance", "itunes". */
  plugin?: string;
  /** Legacy short type code (e.g. 'scan', 'itunes_import'). Old call
   *  sites filter on this; keep as-is. For NEW filters prefer def_id. */
  type: string;
  /** Human-readable label for UI display ("Purge soft-deleted books",
   *  "iTunes Path Repair", etc.). Sourced from the OperationDef
   *  DisplayName on the backend. Use this in render code instead of
   *  type. */
  displayName?: string;
  status: string;
  progress: number;
  total: number;
  message: string;
  startedAt?: number; // timestamp ms
  resumed?: boolean;
  // V2 fields (populated from v2 timeline/SSE)
  parent_id?: string | null;
  current_phase?: string | null;
  current_item?: string | null;
  /** 0 = alert (shows in bell badge), 1 = activity-only (no bell badge) */
  notify_level?: number;
}

export interface OperationLogEvent {
  op_id: string;
  level: string;
  message: string;
  created_at: string;
  sequence: number;
}

interface OperationsState {
  operations: Record<string, ActiveOperation>; // Keyed by id
  activeOperations: ActiveOperation[]; // All ops regardless of notify_level
  /** alertOperations contains only ops with notify_level === 0 (NotifyAlert).
   *  Use this for the bell badge count. */
  alertOperations: ActiveOperation[];
  latestLogEvent: OperationLogEvent | null;
  polling: boolean;
  // SSE EventSource instance — kept here so it can be closed on unmount.
  _sseSource: EventSource | null;

  startPolling: (operationId: string, type: string, resumed?: boolean) => void;
  /** beginOptimistic inserts a placeholder queued op IMMEDIATELY so the bell
   *  reflects user intent before any network round-trip completes.
   *  Returns a synthetic id ("optimistic-…") that callers must hand back to
   *  reconcileOptimistic once the server returns the real operation id. */
  beginOptimistic: (type: string) => string;
  /** reconcileOptimistic finalises a placeholder. If realId is non-null the
   *  placeholder is renamed in place (so SSE updates land on it) and the
   *  normal startPolling side-effects (toast, server refresh) fire. If
   *  realId is null the placeholder is removed silently — used when the
   *  API call failed or returned no operation_id (e.g. nothing to do). */
  reconcileOptimistic: (tempId: string, realId: string | null) => void;
  removeOperation: (operationId: string) => void;
  updateOperation: (op: ActiveOperation) => void;
  loadFromServer: () => Promise<void>;
  // openSSE opens the SSE connection and subscribes to op.* events.
  // Calling it again while a connection is already open is a no-op.
  openSSE: () => void;
  // closeSSE tears down the SSE connection.
  closeSSE: () => void;
}

// Converts v2 operation to unified ActiveOperation.
//
// The legacy `type` field used to be set from `op.plugin`, which made every
// scheduler op render as "scheduler" (the plugin namespace) in the UI even
// though the def_id and display_name carried the real identity. We now
// derive `type` from the def_id's tail segment ("scheduler.purge-deleted"
// → "purge-deleted") so existing equality checks like
// `op.type === 'scan'` keep working when the def_id is just "scan", while
// the bare-plugin display is fixed. The full def_id and the curated
// display_name are also exposed for code that wants them.
function fromV2(op: api.OperationV2): ActiveOperation {
  const defID = op.def_id ?? '';
  // Strip plugin prefix for back-compat with old `op.type === 'scan'` checks.
  const shortType = defID.includes('.') ? defID.slice(defID.indexOf('.') + 1) : defID || op.plugin;
  return {
    id: op.id,
    def_id: defID,
    plugin: op.plugin,
    type: shortType,
    displayName: op.display_name,
    status: op.status,
    progress: op.progress_current ?? 0,
    total: op.progress_total ?? 0,
    message: op.progress_message ?? op.display_name ?? '',
    parent_id: op.parent_id,
    current_phase: op.current_phase,
    current_item: op.current_item,
    notify_level: op.notify_level ?? 0,
  };
}

// Throttle flag for the "unknown op id → reload timeline" path. Without it,
// a server-resumed op that emits a burst of op.updated events would trigger
// one loadFromServer per event until the first reload returns. We coalesce
// to a single reload per 500ms window.
let unknownOpReloadPending = false;
let logEventSequence = 0;

function formatOpLabel(type: string): string {
  const labels: Record<string, string> = {
    itunes_import: 'iTunes Import',
    itunes_sync: 'iTunes Sync',
    scan: 'Library Scan',
    organize: 'Organize',
    metadata_fetch: 'Metadata Fetch',
    metadata_candidate_fetch: 'Metadata Fetch (Batch)',
    bulk_write_back: 'Tag Write-back',
    composer_tag_scan: 'Composer Tag Scan',
    isbn_enrichment: 'ISBN Enrichment',
    metadata_refresh: 'Metadata Refresh',
    reconcile_scan: 'Reconcile Scan',
    itunes_path_repair: 'iTunes Path Repair',
    series_normalize: 'Series Normalize',
    transcode: 'Transcode',
    ol_dump_import: 'Open Library Import',
    'dedup-scan': 'Dedup Scan',
    'dedup-llm-review': 'Dedup AI Review',
    'dedup-acoustid-scan': 'AcoustID Scan',
    'dedup-book-signature-scan': 'Book Signature Scan',
    'embed-scan': 'Embedding Rescan',
    'fingerprint-rescan': 'Fingerprint Rescan',
  };
  return labels[type] ?? type.replace(/_/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase());
}

// Helper to derive activeOperations and alertOperations arrays from operations map
function deriveOperationArrays(operations: Record<string, ActiveOperation>): {
  activeOperations: ActiveOperation[];
  alertOperations: ActiveOperation[];
} {
  const all = Object.values(operations);
  return {
    activeOperations: all,
    alertOperations: all.filter((op) => (op.notify_level ?? 0) === 0),
  };
}

export const useOperationsStore = create<OperationsState>()((set, get) => ({
  operations: {},
  activeOperations: [],
  alertOperations: [],
  latestLogEvent: null,
  polling: false,
  _sseSource: null,

  loadFromServer: async () => {
    try {
      // Load exclusively from v2 timeline endpoint.
      const v2Ops = await api.getOperationTimeline(15);

      set(() => {
        const merged: Record<string, ActiveOperation> = {};

        for (const op of v2Ops) {
          merged[op.id] = fromV2(op);
        }

        return {
          operations: merged,
          ...deriveOperationArrays(merged),
        };
      });
    } catch (err) {
      console.error('Failed to load operations from server', err);
    }
  },

  startPolling: (operationId: string, type: string, resumed = false) => {
    const label = formatOpLabel(type);
    const notify = useAppStore.getState().addNotification;

    notify(resumed ? `${label} resumed` : `${label} started`, 'info');

    // Insert an optimistic entry so the bell shows activity immediately.
    // The SSE op.created/op.updated events will update it with real data.
    const op: ActiveOperation = {
      id: operationId,
      type,
      status: 'queued',
      progress: 0,
      total: 0,
      message: resumed ? 'Resuming...' : 'Starting...',
      startedAt: Date.now(),
      resumed,
    };

    set((state) => {
      const operations = { ...state.operations, [operationId]: op };
      return {
        operations,
        ...deriveOperationArrays(operations),
        polling: true,
      };
    });

    // Trigger a server load shortly after to catch any v2 op registration.
    const loadTimer = setTimeout(() => get().loadFromServer(), 1500);
    // Store for potential cleanup if needed
    return () => clearTimeout(loadTimer);
  },

  beginOptimistic: (type: string) => {
    // crypto.randomUUID is available in all modern browsers; fall back to a
    // timestamp-based id in the unlikely test/SSR case where it is absent.
    const rand =
      typeof globalThis.crypto?.randomUUID === 'function'
        ? globalThis.crypto.randomUUID()
        : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
    const tempId = `optimistic-${rand}`;
    const op: ActiveOperation = {
      id: tempId,
      type,
      status: 'queued',
      progress: 0,
      total: 0,
      message: 'Starting…',
      startedAt: Date.now(),
    };
    set((state) => {
      const operations = { ...state.operations, [tempId]: op };
      return {
        operations,
        ...deriveOperationArrays(operations),
        polling: true,
      };
    });

    // Fire the "started" toast IMMEDIATELY — the click should produce
    // visible feedback before any network round-trip. The reconcile step
    // does not emit a second toast; the catch path can show "Failed".
    const label = formatOpLabel(type);
    useAppStore.getState().addNotification(`${label} starting…`, 'info');

    return tempId;
  },

  reconcileOptimistic: (tempId: string, realId: string | null) => {
    const existing = get().operations[tempId];
    if (!existing) return;

    if (realId === null) {
      // Failed or no-op — drop the placeholder silently.
      get().removeOperation(tempId);
      return;
    }

    // Rename the placeholder to the real id so SSE op.updated events land
    // on the same entry rather than creating a duplicate.
    set((state) => {
      const { [tempId]: _, ...rest } = state.operations;
      const operations = {
        ...rest,
        [realId]: { ...existing, id: realId },
      };
      return { operations, ...deriveOperationArrays(operations) };
    });

    // Server refresh to pick up the real v2 op metadata (display name,
    // notify_level, progress totals). No additional toast — the "starting…"
    // notification already fired from beginOptimistic.
    const refreshTimer = setTimeout(() => get().loadFromServer(), 1500);
    // Store for potential cleanup if needed
    return () => clearTimeout(refreshTimer);
  },

  removeOperation: (operationId: string) => {
    set((state) => {
      const { [operationId]: _, ...remaining } = state.operations;
      return {
        operations: remaining,
        ...deriveOperationArrays(remaining),
        polling: Object.keys(remaining).length > 0,
      };
    });
  },

  updateOperation: (updated: ActiveOperation) => {
    set((state) => {
      const operations = {
        ...state.operations,
        [updated.id]: updated,
      };
      return {
        operations,
        ...deriveOperationArrays(operations),
      };
    });
  },

  openSSE: () => {
    // Guard: don't open a second connection if one is already active.
    if (get()._sseSource !== null) return;

    const es = api.openOperationsSSE({
      onEvent: (name: OperationSSEEventName, payload: unknown) => {
        const p = payload as Record<string, unknown>;
        const opId = (p?.op_id ?? '') as string;

        if (name === 'op.created') {
          // A new v2 op appeared — re-fetch the full timeline to pick it up.
          get().loadFromServer();
        } else if (name === 'op.updated' && opId) {
          // Partial progress update: merge into existing operation if present.
          // If the op is unknown locally (e.g. server resumed it after a
          // restart, so no op.created ever fired), pull the full timeline
          // so the bar shows up. Throttle the reload to avoid hammering
          // the API when many updates arrive in a burst.
          set((state) => {
            const existing = state.operations[opId];
            if (!existing) {
              if (!unknownOpReloadPending) {
                unknownOpReloadPending = true;
                setTimeout(() => {
                  unknownOpReloadPending = false;
                  get().loadFromServer();
                }, 500);
              }
              return state;
            }
            const updated: ActiveOperation = {
              ...existing,
              progress: (p.progress_current as number | undefined) ?? existing.progress,
              total: (p.progress_total as number | undefined) ?? existing.total,
              message: (p.message as string | undefined) ?? existing.message,
            };
            const operations = { ...state.operations, [opId]: updated };
            return { operations, ...deriveOperationArrays(operations) };
          });
        } else if (name === 'op.terminal' && opId) {
          // Operation reached a terminal state — refresh from server.
          get().loadFromServer();
        } else if (name === 'op.current_item' && opId) {
          // High-frequency ephemeral label: no DB write, just patch in-memory.
          const label = (p.label as string | undefined) ?? '';
          set((state) => {
            const existing = state.operations[opId];
            if (!existing) return state;
            const updated: ActiveOperation = { ...existing, current_item: label || null };
            const operations = { ...state.operations, [opId]: updated };
            return { operations, ...deriveOperationArrays(operations) };
          });
        } else if (name === 'op.log' && opId) {
          const message = (p.message as string | undefined) ?? '';
          if (message) {
            set({
              latestLogEvent: {
                op_id: opId,
                level: (p.level as string | undefined) ?? 'info',
                message,
                created_at: (p.created_at as string | undefined) ?? new Date().toISOString(),
                sequence: ++logEventSequence,
              },
            });
          }
        }
      },
      onError: () => {
        // The native EventSource auto-reconnects on TRANSIENT drops (network
        // blip, laptop sleep/wake, a server restart) — on those it sets
        // readyState back to CONNECTING and keeps this exact `es` alive with
        // no action needed from us. It only reaches CLOSED when it has
        // permanently given up (e.g. a fatal HTTP status). openSSE() is only
        // ever called once per login (App.tsx effect keyed on auth state),
        // with nothing that re-invokes it on error, so unconditionally
        // closing `es` here would kill live reconnect on the first blip.
        //
        // So: only tear down + clear the ref once the browser has truly
        // given up (readyState === CLOSED). That still fixes the leak this
        // guard exists for — on a transient drop the ref stays valid so
        // closeSSE() (e.g. on logout) can still reach and close this same
        // `es`, and the `_sseSource !== null` guard keeps blocking a second
        // openSSE() from spawning a duplicate live source.
        if (es.readyState === EventSource.CLOSED) {
          es.close();
          set({ _sseSource: null });
        }
        // Reconcile after a likely server restart: pull the timeline so any
        // ops the server auto-resumed (which won't emit op.created) show up
        // in the bell again. Delay so we don't fire during transient blips.
        setTimeout(() => get().loadFromServer(), 1500);
      },
    });

    set({ _sseSource: es });
  },

  closeSSE: () => {
    const es = get()._sseSource;
    if (es) {
      es.close();
      set({ _sseSource: null });
    }
  },
}));
