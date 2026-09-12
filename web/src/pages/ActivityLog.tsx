// file: web/src/pages/ActivityLog.tsx
// version: 2.33.0
// guid:b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-09-12
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { describeRevertResult, describeUndoPreflight } from '../utils/revertResult';
import { getUndoPreflight } from '../services/versionApi';
import { useNavigate, useSearchParams } from 'react-router-dom';
import {
  Alert,
  AlertTitle,
  Box,
  Button,
  Chip,
  CircularProgress,
  Collapse,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  IconButton,
  LinearProgress,
  Menu,
  MenuItem,
  Pagination,
  Paper,
  Snackbar,
  Stack,
  Table,
  TableContainer,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  TextField,
  Tooltip,
  Typography,
  useMediaQuery,
  useTheme,
} from '@mui/material';
import RefreshIcon from '@mui/icons-material/Refresh';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import PushPinIcon from '@mui/icons-material/PushPin';
import TimelineIcon from '@mui/icons-material/Timeline';
import ClearIcon from '@mui/icons-material/Clear';
import UndoIcon from '@mui/icons-material/Undo';
import CancelIcon from '@mui/icons-material/Cancel';
import ReplayIcon from '@mui/icons-material/Replay';
import DeleteSweepIcon from '@mui/icons-material/DeleteSweep';
import FilterListIcon from '@mui/icons-material/FilterList';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import ChevronRightIcon from '@mui/icons-material/ChevronRight';
import { fetchActivity, fetchActivitySources, compactActivityLog } from '../services/activityApi';
import type { ActivityEntry, SourceCount } from '../services/activityApi';
import { ApiTimeoutError, isAbortError } from '../utils/apiFetch';
import { BatchActivityEntry } from '../components/BatchActivityEntry';
import * as api from '../services/api';
import { PendingFileOpsBanner } from '../components/PendingFileOpsBanner';
import { usePendingFileOps } from '../hooks/usePendingFileOps';
import { useOperationsStore } from '../stores/useOperationsStore';
import { STORAGE_KEYS } from '../lib/storageKeys';
import { tagChipProps } from '../utils/activityTagColors';
import { isTerminal } from '../utils/operationPolling';
import { operationDisplayName } from '../components/layout/operationsFormat';

const PAGE_SIZE_OPTIONS = [25, 50, 100, 250];

// Section keys for the Active Operations panel, declared here rather than
// derived from the rendered list so "Collapse All" can name every section even
// when one of them is currently empty and therefore not rendered.
// How many operations one section shows at a time. The Completed section runs to
// the full 24-hour window — 80 rows on production — and an unpaged list of that
// buries the sections under it, which on this page includes Failed.
const OPS_SECTION_PAGE_SIZE = 7;

const OPS_SECTION_KEYS = [
  'pending',
  'active',
  'completed',
  'failed',
  'canceled',
  'interrupted',
] as const;

const EVENT_TYPES = [
  'book_added',
  'book_updated',
  'book_deleted',
  'book_restored',
  'tag_written',
  'metadata_applied',
  'scan_started',
  'scan_completed',
  'organize_started',
  'organize_completed',
  'import_started',
  'import_completed',
  'maintenance_run',
  'config_changed',
  'user_action',
];

const TIER_COLORS: Record<string, string> = {
  audit: '#1976d2',
  change: '#9c27b0',
  debug: '#757575',
  digest: '#00897b',
};

function levelChip(level: string) {
  const colorMap: Record<string, 'error' | 'warning' | 'info' | 'success' | 'default'> = {
    error: 'error',
    warn: 'warning',
    warning: 'warning',
    info: 'info',
    debug: 'default',
  };
  return (
    <Chip size="small" label={level} color={colorMap[level] ?? 'default'} variant="outlined" />
  );
}

function rowBgColor(entry: ActivityEntry): string | undefined {
  if (entry.level === 'error') return 'rgba(211, 47, 47, 0.08)';
  if (entry.level === 'warn' || entry.level === 'warning') return 'rgba(237, 108, 2, 0.08)';
  if (entry.summary.startsWith('\u2713')) return 'rgba(46, 125, 50, 0.08)';
  return undefined;
}

const formatTimestamp = (ts: string): string => {
  const d = new Date(ts);
  if (isNaN(d.getTime())) return ts;
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
};

const formatTimestampCompact = (ts: string): string => {
  const d = new Date(ts);
  if (isNaN(d.getTime())) return ts;
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
};

/** Format an ISO timestamp string as HH:MM:SS time-of-day, or '' if missing/zero. */
const formatItemTime = (ts?: string): string => {
  if (!ts) return '';
  const d = new Date(ts);
  if (isNaN(d.getTime()) || d.getFullYear() < 2000) return '';
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
};

const displayTags = (tags?: string[]): string[] =>
  (tags ?? []).filter((tag) => tag !== 'source:server' && tag !== 'action:system');

/** How far back the feed looks when the user has not chosen a range. */
const DEFAULT_SINCE_HOURS = 24;

/** Format a Date as the `YYYY-MM-DDTHH:mm` local string a datetime-local input wants. */
const toDateTimeLocal = (d: Date): string => {
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
};

/** The default "Since" value: DEFAULT_SINCE_HOURS ago, in local time. */
const defaultSinceValue = (): string =>
  toDateTimeLocal(new Date(Date.now() - DEFAULT_SINCE_HOURS * 60 * 60 * 1000));

/**
 * Convert a `datetime-local` input value to the RFC3339 string the API needs.
 *
 * `GET /api/v1/activity` parses `since`/`until` with
 * `time.Parse(time.RFC3339, v)` and returns 400 on anything else. A
 * datetime-local input yields `YYYY-MM-DDTHH:mm` — no seconds, no offset —
 * which is NOT valid RFC3339, so every date filter this page sent was rejected.
 * With no error state on the page, that 400 rendered as "No activity entries
 * found.", i.e. the date filters looked like they worked and silently returned
 * nothing. Returns undefined for empty/unparseable input so the param is
 * omitted rather than sent malformed.
 */
const toRFC3339 = (localValue: string): string | undefined => {
  if (!localValue) return undefined;
  const d = new Date(localValue);
  if (isNaN(d.getTime())) return undefined;
  return d.toISOString();
};

/** Human-readable one-liner for an error surfaced in the UI. */
const describeError = (err: unknown): string => {
  if (err instanceof ApiTimeoutError) {
    return `The server did not respond within ${Math.round(err.timeoutMs / 1000)}s. It may be overloaded — try a narrower time range or fewer entries per page.`;
  }
  if (err instanceof Error) return err.message;
  return String(err);
};

export default function ActivityLog() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const theme = useTheme();
  const isMobile = useMediaQuery(theme.breakpoints.down('sm'));

  // Filters
  const [search, setSearch] = useState('');
  const [tiers, setTiers] = useState<Set<string>>(new Set(['audit', 'change', 'digest', 'info']));
  const [typeFilter, setTypeFilter] = useState('');
  const [levelFilter, setLevelFilter] = useState('');
  const [operationId, setOperationId] = useState('');
  // The feed is bounded to the last DEFAULT_SINCE_HOURS by default. Without a
  // window the page asked the server for ALL history on every load and on
  // every poll tick, which is what let one open tab drive an unbounded scan.
  // This is a VISIBLE default, not a silent cap: it is rendered in the "Since"
  // field, it can be edited, and clearing the field restores all-history.
  const [defaultSince] = useState(defaultSinceValue);
  const [sinceFilter, setSinceFilter] = useState(defaultSince);
  const [untilFilter, setUntilFilter] = useState('');
  const [excludedSources, setExcludedSources] = useState<Set<string>>(() => {
    const saved = localStorage.getItem(STORAGE_KEYS.ACTIVITY_SOURCE_PREFS);
    return saved ? new Set(JSON.parse(saved)) : new Set();
  });
  const [hideNoOp, setHideNoOp] = useState(true);
  const [tagFilter, setTagFilter] = useState<string[]>([]);

  /** Toggle a single tag in the tag filter. Adds if absent, removes if present. */
  const toggleTagFilter = useCallback((tag: string) => {
    setTagFilter((prev) => (prev.includes(tag) ? prev.filter((t) => t !== tag) : [...prev, tag]));
  }, []);

  // Per-section page, keyed by section key. Absent means page 1. Deliberately
  // NOT clamped on write: sections shrink as ops age out of the 24h window, and
  // a stored page past the end would blank the list. Clamping happens at render.
  const [sectionPages, setSectionPages] = useState<Record<string, number>>({});

  // Mobile filter collapse
  const [filtersExpanded, setFiltersExpanded] = useState(false);

  // Pending background file operations (cover embed, tag write, rename)
  const { operations: pendingFileOps } = usePendingFileOps();

  // Active ops from unified store
  // opRows is the WHOLE 24-hour window, finished jobs included, WITH runs of
  // consecutive same-kind operations folded under synthetic parent rows (see
  // operationGrouping.ts). It is what this page RENDERS, and nothing else:
  // a synthetic row's id names no record on the server, so anything that
  // resolves an operation by id must read activeOperations instead. liveOps is
  // the ungrouped subset still running, and is what anything labelled "active"
  // or gated on real work must read.
  const opRows = useOperationsStore((state) => state.groupedOperations);
  // rawOps is the same window UNGROUPED — one entry per real operation, no
  // synthetic rows. Every COUNT on this page comes from here (see sectionDefs);
  // opRows is only ever for rendering.
  const rawOps = useOperationsStore((state) => state.activeOperations);
  const liveOps = useOperationsStore((state) => state.liveOperations);
  const loadActiveOpsFromServer = useOperationsStore((state) => state.loadFromServer);
  // Set when the last timeline refresh failed. opRows is then the last list the
  // server confirmed, and an EMPTY opRows means "unknown", not "idle" (WEB-05).
  const opsLoadError = useOperationsStore((state) => state.loadError);
  const latestLogEvent = useOperationsStore((state) => state.latestLogEvent);
  const [pinned, setPinned] = useState(
    () => localStorage.getItem(STORAGE_KEYS.ACTIVITY_OPS_PINNED) !== 'false'
  );
  const [cancelling, setCancelling] = useState<Set<string>>(new Set());
  // Ids with a Discard request in flight. Separate from `cancelling`: the
  // running-row Cancel button reads that set to label itself "Cancelling...",
  // and a discard is not a cancel.
  const [discarding, setDiscarding] = useState<Set<string>>(new Set());
  const [expandedOpId, setExpandedOpId] = useState<string | null>(searchParams.get('op'));
  // pausedByExpand: true when auto-refresh was auto-paused because a row is
  // expanded. Cleared when the row collapses or the user clicks "Follow log".
  const pausedByExpandRef = useRef(false);
  const [opLogs, setOpLogs] = useState<string[]>([]);
  // opLogsLoaded distinguishes "haven't fetched yet" from "fetched, empty".
  // Without this, an op with zero logs shows "Loading logs..." forever.
  const [opLogsLoaded, setOpLogsLoaded] = useState(false);

  // Tree collapse state: set of parent op IDs that are collapsed.
  // Parents with ≥3 children start collapsed; new Set() unambiguously means
  // "all expanded" (not "use defaults").
  const [collapsedParents, setCollapsedParents] = useState<Set<string>>(new Set());
  // Every parent this page has ALREADY applied the default to. A parent is
  // seeded once and never again, so "Expand All" and a manual expand both stick
  // across the 5-second poll that re-derives the groups.
  const seededParentsRef = useRef<Set<string>>(new Set());

  // Section collapse state: which of the Pending/Active/Completed groups are
  // rolled up. This is what Expand All / Collapse All actually act on.
  //
  // Those buttons used to drive collapsedParents alone, which made them dead
  // controls: parent/child op nesting is only ever populated by
  // registry.WithParent, and that helper has no production callers, so every
  // operation the API returns has parent_id: null. Confirmed against prod on
  // 2026-09-08 — 40 operations over 6 hours, every one of them parentless.
  // Both buttons therefore recomputed a set that no rendered row consulted,
  // and clicking either did nothing visible. They still clear/refill
  // collapsedParents so they stay correct if op lineage is ever wired up.
  //
  // On first load only Active is open. The panel spans 24 hours, so the
  // finished sections hold most of what is in it, and with all of them open
  // the running work — the one thing that changes while you watch — was
  // pushed below a screen of history. The user's toggles are not persisted;
  // this is the starting point, not a preference.
  const [collapsedSections, setCollapsedSections] = useState<Set<string>>(
    () => new Set(OPS_SECTION_KEYS.filter((key) => key !== 'active'))
  );
  // One-line feedback for the per-row Retry / Discard actions. The page has no
  // toast provider, so this is a plain Snackbar; null means closed.
  const [toast, setToast] = useState<string | null>(null);
  const opLogsRef = useRef<HTMLDivElement>(null);

  // Sources
  const [sources, setSources] = useState<SourceCount[]>([]);
  const [sourcesOpen, setSourcesOpen] = useState(false);
  const [sourcesError, setSourcesError] = useState<string | null>(null);

  // Feed
  const [entries, setEntries] = useState<ActivityEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(25);
  const [loading, setLoading] = useState(false);
  // Silent background refresh — updates data without destroying the table DOM
  const [refreshing, setRefreshing] = useState(false);
  // Feed error. Distinct from "no entries": a 500, a 401, a timeout and a
  // genuinely empty log used to render the identical "No activity entries
  // found." message, so a hard failure was indistinguishable from success.
  const [error, setError] = useState<string | null>(null);

  // Auto-refresh
  const [autoRefresh, setAutoRefresh] = useState(true);
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);

  // Compact
  const [compactAnchor, setCompactAnchor] = useState<null | HTMLElement>(null);
  const [compacting, setCompacting] = useState(false);
  // Outcome of the last Compact click. The endpoint only STARTS the job
  // (202 + op id); the work itself is watched in the operations list above.
  const [compactNotice, setCompactNotice] = useState<{
    severity: 'success' | 'error';
    message: string;
  } | null>(null);
  const [customCompactDays, setCustomCompactDays] = useState('');
  const [expandedDigests, setExpandedDigests] = useState<Set<string>>(new Set());

  // Revert dialog
  const [revertEntry, setRevertEntry] = useState<ActivityEntry | null>(null);
  // The preflight-derived confirmation for revertEntry: the real restorable
  // count and the record-only rows, never a blanket "undo all changes".
  const [revertPlan, setRevertPlan] = useState('');
  const [reverting, setReverting] = useState(false);

  // Refs for intervals
  const opsIntervalRef = useRef<number | null>(null);
  const feedIntervalRef = useRef<number | null>(null);
  const sourcesDropdownRef = useRef<HTMLDivElement | null>(null);

  // ---- Request lifecycle bookkeeping -------------------------------------
  // Polls used to fire on a fixed schedule regardless of whether the previous
  // request had come back, so a slow /activity query meant requests stacked up
  // and each one kept server memory alive — one open tab was enough to grow
  // without bound. Three pieces of state fix that, per endpoint:
  //   *InFlightRef  — a background poll drops its tick entirely if a request
  //                   is still outstanding. Polls never stack.
  //   *AbortRef     — a user-driven load (mount, filter, page, Refresh)
  //                   supersedes instead of waiting: the older request's
  //                   answer is already wrong, so it is aborted.
  //   *SeqRef       — monotonic ticket. A superseded request that finishes
  //                   late must not write state belonging to the request that
  //                   replaced it (including clearing its spinner).
  const feedInFlightRef = useRef(false);
  const feedAbortRef = useRef<AbortController | null>(null);
  const feedSeqRef = useRef(0);
  const sourcesInFlightRef = useRef(false);
  const sourcesAbortRef = useRef<AbortController | null>(null);
  const sourcesSeqRef = useRef(0);

  // Cancel anything outstanding on unmount — otherwise navigating away leaves
  // the server finishing a query nobody will ever read.
  useEffect(
    () => () => {
      feedAbortRef.current?.abort();
      sourcesAbortRef.current?.abort();
    },
    []
  );

  // Tracks pending scroll timers so they can be cancelled on unmount — a timer
  // that fires after unmount would touch a detached ref (harmless here thanks to
  // optional chaining, but still flagged by the memory-leak scanner).
  const scrollTimeoutsRef = useRef<number[]>([]);
  useEffect(
    () => () => {
      scrollTimeoutsRef.current.forEach((id) => window.clearTimeout(id));
      scrollTimeoutsRef.current = [];
    },
    []
  );

  // Auto-pause refresh when a row is expanded so log lines don't jump away.
  // Restores the previous state when the row collapses, unless the user
  // manually clicked "Follow log" (which clears pausedByExpandRef).
  useEffect(() => {
    if (expandedOpId !== null) {
      if (autoRefresh) {
        setAutoRefresh(false);
        pausedByExpandRef.current = true;
      }
    } else if (pausedByExpandRef.current) {
      setAutoRefresh(true);
      pausedByExpandRef.current = false;
    }
    // intentionally omit autoRefresh — only trigger on expand/collapse transitions
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [expandedOpId]);

  // Persist excluded sources
  useEffect(() => {
    localStorage.setItem(STORAGE_KEYS.ACTIVITY_SOURCE_PREFS, JSON.stringify([...excludedSources]));
  }, [excludedSources]);

  // Persist pin state
  useEffect(() => {
    localStorage.setItem(STORAGE_KEYS.ACTIVITY_OPS_PINNED, String(pinned));
  }, [pinned]);

  // Default-collapse every parent with ≥3 children, ONCE PER PARENT.
  //
  // Not once per page, which is what the old collapsedInitializedRef guard did.
  // That guard was correct while parent_id only ever came from the server (it
  // never did — registry.WithParent has no production callers), but synthetic
  // groups are re-derived on every 5-second poll and new ones appear as
  // operations run. A once-per-page seed would leave every group that appeared
  // after the first render fully expanded, which is the row flood this change
  // exists to stop.
  //
  // seededParentsRef is what keeps that from fighting the user: a parent is
  // seeded exactly once, so expanding a group makes it stay expanded even
  // though the seed effect re-runs on the next poll. Group ids are derived from
  // content, not minted, so "already seeded" survives a refresh — see
  // operationGrouping.groupId.
  useEffect(() => {
    if (opRows.length === 0) return;
    const childrenCount: Record<string, number> = {};
    for (const op of opRows) {
      if (op.parent_id) {
        childrenCount[op.parent_id] = (childrenCount[op.parent_id] ?? 0) + 1;
      }
    }
    const fresh: string[] = [];
    for (const [id, count] of Object.entries(childrenCount)) {
      if (count >= 3 && !seededParentsRef.current.has(id)) {
        seededParentsRef.current.add(id);
        fresh.push(id);
      }
    }
    if (fresh.length > 0) {
      setCollapsedParents((prev) => new Set([...prev, ...fresh]));
    }
  }, [opRows]);

  const loadOperationLogs = useCallback(async (opId: string) => {
    const logs = await api.getOperationLogs(opId);
    setOpLogs(logs.map((l: { message?: string }) => l.message || String(l)));
    setOpLogsLoaded(true);
    const scrollTimeout = window.setTimeout(
      () => opLogsRef.current?.scrollTo({ top: opLogsRef.current.scrollHeight }),
      50
    );
    scrollTimeoutsRef.current.push(scrollTimeout);
  }, []);

  // Load logs once when an operation is expanded. Live lines append via SSE
  // below; the per-op refresh button is the explicit full reload path.
  useEffect(() => {
    if (!expandedOpId) {
      setOpLogs([]);
      setOpLogsLoaded(false);
      return;
    }
    setOpLogsLoaded(false);
    let cancelled = false;
    const fetchLogs = async () => {
      try {
        if (!cancelled) {
          await loadOperationLogs(expandedOpId);
        }
      } catch {
        if (!cancelled) {
          setOpLogs(['Failed to load logs']);
          setOpLogsLoaded(true);
        }
      }
    };
    fetchLogs();
    return () => {
      cancelled = true;
    };
  }, [expandedOpId, loadOperationLogs]);

  useEffect(() => {
    if (!latestLogEvent || latestLogEvent.op_id !== expandedOpId) return;
    setOpLogsLoaded(true);
    // Cap retained log lines to avoid unbounded growth on long-running ops
    // (mirrors OperationActivityPanel's per-op cap).
    setOpLogs((prev) => [...prev, latestLogEvent.message].slice(-1000));
    const scrollTimeout = window.setTimeout(
      () => opLogsRef.current?.scrollTo({ top: opLogsRef.current.scrollHeight }),
      50
    );
    scrollTimeoutsRef.current.push(scrollTimeout);
  }, [latestLogEvent, expandedOpId]);

  // Load sources. Pass silent=true from the auto-refresh tick so it can be
  // dropped rather than stacked when the previous one has not returned.
  const loadSources = useCallback(
    async (silent = false) => {
      if (silent) {
        if (sourcesInFlightRef.current) return;
      } else {
        sourcesAbortRef.current?.abort();
      }
      const controller = new AbortController();
      sourcesAbortRef.current = controller;
      sourcesInFlightRef.current = true;
      const seq = ++sourcesSeqRef.current;
      const isCurrent = () => seq === sourcesSeqRef.current;

      try {
        const data = await fetchActivitySources(
          {
            since: toRFC3339(sinceFilter),
            until: toRFC3339(untilFilter),
          },
          { signal: controller.signal }
        );
        if (!isCurrent()) return;
        setSources(data.sources || []);
        setSourcesError(null);
      } catch (err) {
        // A supersede/unmount abort is normal control flow, not a failure.
        if (isAbortError(err) || !isCurrent()) return;
        console.error('Failed to load sources', err);
        setSourcesError(describeError(err));
      } finally {
        if (isCurrent()) {
          sourcesInFlightRef.current = false;
          sourcesAbortRef.current = null;
        }
      }
    },
    [sinceFilter, untilFilter]
  );

  // Load activity feed. Pass silent=true for background refreshes to avoid
  // replacing the table with a spinner (which resets scroll position).
  const loadFeed = useCallback(
    async (p: number, silent = false) => {
      // The guard lives HERE, not in the polling effect, so that every entry
      // point is covered: the effects, the Refresh button, revert and compact
      // all call loadFeed directly.
      if (silent) {
        if (feedInFlightRef.current) return;
      } else {
        feedAbortRef.current?.abort();
      }
      const controller = new AbortController();
      feedAbortRef.current = controller;
      feedInFlightRef.current = true;
      const seq = ++feedSeqRef.current;
      const isCurrent = () => seq === feedSeqRef.current;

      if (silent) {
        setRefreshing(true);
      } else {
        setLoading(true);
      }
      try {
        const excludeStr = excludedSources.size > 0 ? [...excludedSources].join(',') : undefined;

        // Server-side tier filtering via exclude_tiers
        const allTiers = ['audit', 'change', 'debug', 'digest'];
        const inactiveTiers = allTiers.filter((t) => !tiers.has(t));
        const excludeTiersStr = inactiveTiers.length > 0 ? inactiveTiers.join(',') : undefined;

        const result = await fetchActivity(
          {
            limit: pageSize,
            offset: (p - 1) * pageSize,
            type: typeFilter || undefined,
            level: levelFilter || undefined,
            operation_id: operationId.trim() || undefined,
            since: toRFC3339(sinceFilter),
            until: toRFC3339(untilFilter),
            search: search.trim() || undefined,
            exclude_sources: excludeStr,
            exclude_tiers: excludeTiersStr,
            exclude_tags: hideNoOp ? 'no-op' : undefined,
            tags: tagFilter.length > 0 ? tagFilter.join(',') : undefined,
          },
          { signal: controller.signal }
        );

        if (!isCurrent()) return;
        setEntries(result.entries || []);
        setTotal(result.total || 0);
        setError(null);
        setLastUpdated(new Date());
      } catch (err) {
        // Our own abort (supersede / unmount) is not a failure — reporting it
        // would flash an error panel on every keystroke in the search box.
        if (isAbortError(err) || !isCurrent()) return;
        console.error('Failed to load activity', err);
        setError(describeError(err));
        // A failed BACKGROUND refresh must not destroy what the user is reading:
        // keep the last good page and surface the failure as a banner. Only a
        // failed foreground load clears the table, because in that case there is
        // nothing valid left to show.
        if (!silent) {
          setEntries([]);
          setTotal(0);
        }
      } finally {
        if (isCurrent()) {
          feedInFlightRef.current = false;
          feedAbortRef.current = null;
          setLoading(false);
          setRefreshing(false);
        }
      }
    },
    [
      typeFilter,
      levelFilter,
      operationId,
      sinceFilter,
      untilFilter,
      search,
      excludedSources,
      tiers,
      hideNoOp,
      tagFilter,
      pageSize,
    ]
  );

  // Initial load + polling for active ops (3s when Activity page is mounted or
  // bell is open). The interval was unconditional before — toggling
  // Auto-refresh OFF didn't actually stop it, which made the toggle a lie.
  // Now it respects autoRefresh: initial fetch always runs, interval only
  // arms when autoRefresh is on.
  useEffect(() => {
    loadActiveOpsFromServer();
    if (opsIntervalRef.current) window.clearInterval(opsIntervalRef.current);
    if (autoRefresh) {
      opsIntervalRef.current = window.setInterval(loadActiveOpsFromServer, 3000);
    }
    return () => {
      if (opsIntervalRef.current) window.clearInterval(opsIntervalRef.current);
      opsIntervalRef.current = null;
    };
  }, [loadActiveOpsFromServer, autoRefresh]);

  // Sources load on their own schedule. fetchActivitySources only varies with
  // the time window, so keeping it out of the feed effect stops it re-firing
  // on every page change.
  useEffect(() => {
    loadSources();
  }, [loadSources]);

  // SINGLE feed loader.
  //
  // There used to be two effects here — one keyed on the filters, one on
  // page/pageSize — and BOTH ran on mount, so every mount fired the expensive
  // /activity query twice. loadFeed's identity already changes whenever any
  // filter or pageSize changes, so one effect keyed on (loadFeed, page)
  // covers both cases with exactly one fetch.
  const prevLoadFeedRef = useRef<typeof loadFeed | null>(null);
  useEffect(() => {
    const filtersChanged = prevLoadFeedRef.current !== null && prevLoadFeedRef.current !== loadFeed;
    prevLoadFeedRef.current = loadFeed;
    if (filtersChanged && page !== 1) {
      // Changing a filter resets to page 1. Fetch nothing now: setPage re-runs
      // this effect, and fetching the old page here would be a second query
      // that is superseded a moment later — the exact duplicate this
      // consolidation removes.
      setPage(1);
      return;
    }
    loadFeed(page);
  }, [loadFeed, page]);

  // Auto-refresh feed — 5s when active ops exist, 30s when idle.
  // Uses silent=true so the table stays in the DOM and scroll position is preserved.
  //
  // liveOps, not opRows: the latter counts a whole day of FINISHED jobs, so
  // this sat at the 5s rate permanently on an idle server — a poll every five
  // seconds forever for history that cannot change.
  const refreshInterval = liveOps.length > 0 ? 5000 : 30000;
  useEffect(() => {
    if (feedIntervalRef.current) window.clearInterval(feedIntervalRef.current);
    if (autoRefresh) {
      feedIntervalRef.current = window.setInterval(() => {
        // silent=true is what makes a tick DROPPABLE. If the previous request
        // has not returned, these calls no-op instead of stacking a second
        // full-scan query on top of the first.
        loadFeed(page, true);
        loadSources(true);
      }, refreshInterval);
    }
    return () => {
      if (feedIntervalRef.current) window.clearInterval(feedIntervalRef.current);
      feedIntervalRef.current = null;
    };
  }, [autoRefresh, page, refreshInterval, loadFeed, loadSources]);

  // Close sources dropdown on outside click
  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (sourcesDropdownRef.current && !sourcesDropdownRef.current.contains(e.target as Node)) {
        setSourcesOpen(false);
      }
    };
    if (sourcesOpen) {
      document.addEventListener('mousedown', handler);
      return () => document.removeEventListener('mousedown', handler);
    }
  }, [sourcesOpen]);

  const handleRefresh = () => {
    loadFeed(page);
    loadActiveOpsFromServer();
    loadSources();
  };

  // Cancel and Discard both report a server refusal in the toast. They used to
  // log it to the console only, so a 404 on a row the server had already
  // finished looked like a button that did nothing — fifteen presses on
  // 2026-09-10 before anyone opened the console.
  const handleCancelOp = async (opId: string) => {
    setCancelling((prev) => new Set(prev).add(opId));
    try {
      await api.cancelOperation(opId);
      await loadActiveOpsFromServer();
    } catch (err) {
      console.error('Failed to cancel operation', err);
      setToast(describeError(err));
    }
    setCancelling((prev) => {
      const next = new Set(prev);
      next.delete(opId);
      return next;
    });
  };

  // Discard deletes the run's record outright (DELETE /operations/v2/:id/record)
  // — not a cancel. An interrupted row is either one the server may still
  // resume on the next restart or one it has already dropped; both are
  // finished from the user's point of view, and removing the record is the
  // one action that works for both and gets the row out of the list.
  const handleDiscardOp = async (opId: string) => {
    setDiscarding((prev) => new Set(prev).add(opId));
    try {
      await api.discardOperation(opId);
      // The record is gone: an expanded log panel or a ?op= deep link for it
      // would otherwise keep fetching logs for an id the server now 404s.
      if (expandedOpId === opId) {
        setExpandedOpId(null);
      }
      await loadActiveOpsFromServer();
      setToast('Operation discarded');
    } catch (err) {
      console.error('Failed to discard operation', err);
      setToast(describeError(err));
    }
    setDiscarding((prev) => {
      const next = new Set(prev);
      next.delete(opId);
      return next;
    });
  };

  const handleClearStale = async () => {
    try {
      await api.clearStaleOperations();
      await loadActiveOpsFromServer();
    } catch (err) {
      console.error('Failed to clear stale operations', err);
    }
  };

  // Per-op manual refresh: forces a server fetch for just this op's progress
  // / status without waiting for the auto-refresh tick. Useful when the user
  // has Auto-refresh OFF but wants the truth on one op. Offered only on rows
  // that can still change — a finished op has nothing to refresh, and those
  // rows get Retry instead.
  const handleRefreshOp = async (_opId: string) => {
    try {
      await loadActiveOpsFromServer();
      if (expandedOpId === _opId) {
        await loadOperationLogs(_opId);
      }
    } catch (err) {
      console.error('Failed to refresh op', err);
    }
  };

  // Per-op retry: requeues a failed / canceled / interrupted op as a NEW run
  // and reloads the list so the queued row appears. The toast carries the new
  // id so the user can find it; the failure toast carries the server's reason
  // (a def with no retry, an op still running, ...) rather than a generic one.
  const handleRetryOp = async (opId: string) => {
    try {
      const requeued = await api.retryOperation(opId);
      await loadActiveOpsFromServer();
      setToast(`Requeued as ${requeued.id.slice(0, 8)}`);
    } catch (err) {
      console.error('Failed to retry operation', err);
      setToast(describeError(err));
    }
  };

  // Per-op copy: plain-text summary suitable for pasting into a bug report
  // or sharing with another claude session — id, def, status, progress,
  // message, timestamps.
  const handleCopyOp = async (op: (typeof opRows)[0]) => {
    const lines = [
      `id:       ${op.id}`,
      `def:      ${op.def_id ?? op.type}`,
      `name:     ${operationDisplayName(op)}`,
      `status:   ${op.status}`,
      `progress: ${op.progress} / ${op.total}` +
        (op.total > 0 ? ` (${((op.progress / op.total) * 100).toFixed(2)}%)` : ''),
      `message:  ${op.message ?? ''}`,
    ];
    try {
      await navigator.clipboard.writeText(lines.join('\n'));
    } catch (err) {
      console.error('Failed to copy op summary', err);
    }
  };

  // Ask the server what the revert would actually do before offering it, as
  // OperationsIndicator does. When nothing is restorable (a purge op whose rows
  // are all author_delete records) the reason is shown and no dialog opens, so
  // the user is never asked to confirm an Undo the server will refuse with 409.
  const openRevert = async (target: ActivityEntry) => {
    if (!target.operation_id) return;
    try {
      const plan = describeUndoPreflight(await getUndoPreflight(target.operation_id));
      if (!plan.canUndo) {
        setToast(plan.message);
        return;
      }
      setRevertPlan(plan.message);
      setRevertEntry(target);
    } catch (err) {
      console.error('Failed to load undo preflight', err);
      setToast(describeError(err));
    }
  };

  const handleRevert = async () => {
    if (!revertEntry?.operation_id) return;
    setReverting(true);
    try {
      const result = await api.revertOperation(revertEntry.operation_id);
      setToast(describeRevertResult(result));
      loadFeed(page);
    } catch (err) {
      // A 409 (nothing restorable) or 500 carries the server's reason. The
      // dialog still closes in `finally`, so this toast is where the user
      // learns the revert did not happen, or only partly did.
      console.error('Failed to revert operation', err);
      setToast(describeError(err));
    } finally {
      setReverting(false);
      setRevertEntry(null);
    }
  };

  const handleCompact = async (days: number) => {
    setCompactAnchor(null);
    setCompacting(true);
    try {
      // 202: the compaction is now a background operation. Nothing to await
      // here beyond the enqueue — reload the operations list so the new op
      // shows at the top with its live log, and tell the user where to look.
      const started = await compactActivityLog(days);
      setCompactNotice({
        severity: 'success',
        message: `Compaction started (operation ${started.operation_id}) — follow it in the operations list`,
      });
      void loadActiveOpsFromServer();
    } catch (err) {
      setCompactNotice({
        severity: 'error',
        message: err instanceof Error ? err.message : `Compaction failed: ${String(err)}`,
      });
    } finally {
      setCompacting(false);
    }
  };

  const toggleTier = (tier: string) => {
    setTiers((prev) => {
      const next = new Set(prev);
      if (next.has(tier)) {
        next.delete(tier);
      } else {
        next.add(tier);
      }
      return next;
    });
  };

  const hasActiveFilters =
    search !== '' ||
    tiers.size !== 3 ||
    !tiers.has('audit') ||
    !tiers.has('change') ||
    !tiers.has('digest') ||
    typeFilter !== '' ||
    levelFilter !== '' ||
    operationId !== '' ||
    // Compared against the DEFAULT window, not '': with a default "Since" the
    // old `!== ''` test would latch permanently on and "Clear filters" would
    // never disappear.
    sinceFilter !== defaultSince ||
    untilFilter !== '' ||
    excludedSources.size > 0 ||
    !hideNoOp;

  // Count active non-search filters (for mobile badge)
  const activeFilterCount = [
    tiers.size !== 3 || !tiers.has('audit') || !tiers.has('change') || !tiers.has('digest'),
    typeFilter !== '',
    levelFilter !== '',
    operationId !== '',
    sinceFilter !== defaultSince,
    untilFilter !== '',
    excludedSources.size > 0,
    !hideNoOp,
  ].filter(Boolean).length;

  const clearFilters = () => {
    setSearch('');
    setTiers(new Set(['audit', 'change', 'digest']));
    setTypeFilter('');
    setLevelFilter('');
    setOperationId('');
    // Back to the DEFAULT window, not all-history: resetting to '' would turn
    // "Clear filters" into a one-click unbounded-query button.
    setSinceFilter(defaultSince);
    setUntilFilter('');
    setExcludedSources(new Set());
    setHideNoOp(true);
  };

  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  // A failed refresh keeps the section visible even when unpinned and empty:
  // hiding it would hide the only place the failure is reported.
  const showOpsSection = pinned || opRows.length > 0 || opsLoadError !== null;

  // Shared filter controls (used in both mobile collapsed and desktop layouts)
  const tierChips = (
    <Stack
      direction="row"
      spacing={1}
      sx={{
        flexWrap: 'wrap',
      }}
    >
      {['audit', 'change', 'debug', 'digest'].map((tier) => (
        <Chip
          key={tier}
          label={tiers.has(tier) ? `\u2713 ${tier}` : tier}
          onClick={() => toggleTier(tier)}
          variant={tiers.has(tier) ? 'filled' : 'outlined'}
          sx={{
            borderColor: tiers.has(tier) ? TIER_COLORS[tier] : undefined,
            borderWidth: tiers.has(tier) ? 2 : 1,
            color: tiers.has(tier) ? TIER_COLORS[tier] : undefined,
            fontWeight: tiers.has(tier) ? 'bold' : 'normal',
            cursor: 'pointer',
          }}
        />
      ))}
      <Chip
        label={hideNoOp ? '\u2713 hide no-op' : 'show no-op'}
        onClick={() => setHideNoOp((v) => !v)}
        variant={hideNoOp ? 'filled' : 'outlined'}
        sx={[
          {
            cursor: 'pointer',
          },
          hideNoOp
            ? {
                borderWidth: 2,
              }
            : {
                borderWidth: 1,
              },
          hideNoOp
            ? {
                opacity: 1,
              }
            : {
                opacity: 0.6,
              },
        ]}
      />
    </Stack>
  );

  const sourcesButton = (fullWidth?: boolean) => (
    <Box
      sx={[
        {
          position: 'relative',
        },
        fullWidth
          ? {
              width: '100%',
            }
          : {
              width: null,
            },
      ]}
      ref={sourcesDropdownRef}
    >
      <Button
        size="small"
        variant="outlined"
        onClick={() => setSourcesOpen(!sourcesOpen)}
        fullWidth={fullWidth}
      >
        Sources
        {excludedSources.size > 0 && (
          <Chip
            size="small"
            label={`-${excludedSources.size}`}
            color="warning"
            sx={{ ml: 0.5, height: 20, fontSize: '0.7rem' }}
          />
        )}
      </Button>
      {sourcesOpen && (
        <Paper
          elevation={8}
          sx={{
            position: 'absolute',
            top: '100%',
            left: 0,
            zIndex: 10,
            minWidth: 280,
            maxHeight: 400,
            overflow: 'auto',
            mt: 0.5,
            p: 1,
          }}
        >
          {sources.length === 0 ? (
            <Typography
              variant="body2"
              sx={{
                color: 'text.secondary',
                p: 1,
              }}
            >
              No sources found.
            </Typography>
          ) : (
            sources.map((s) => {
              const isExcluded = excludedSources.has(s.source);
              return (
                <Stack
                  key={s.source}
                  direction="row"
                  spacing={1}
                  onClick={() => {
                    setExcludedSources((prev) => {
                      const next = new Set(prev);
                      if (isExcluded) {
                        next.delete(s.source);
                      } else {
                        next.add(s.source);
                      }
                      return next;
                    });
                  }}
                  sx={{
                    alignItems: 'center',
                    px: 1,
                    py: 0.5,
                    cursor: 'pointer',
                    '&:hover': { bgcolor: 'action.hover' },
                  }}
                >
                  <input
                    type="checkbox"
                    checked={!isExcluded}
                    readOnly
                    style={{ pointerEvents: 'none' }}
                  />
                  <Typography
                    variant="body2"
                    sx={[
                      {
                        flexGrow: 1,
                      },
                      isExcluded
                        ? {
                            textDecoration: 'line-through',
                          }
                        : {
                            textDecoration: 'none',
                          },
                      isExcluded
                        ? {
                            opacity: 0.5,
                          }
                        : {
                            opacity: 1,
                          },
                    ]}
                  >
                    {s.source}
                  </Typography>
                  <Typography
                    variant="caption"
                    sx={{
                      color: 'text.secondary',
                    }}
                  >
                    {s.count}
                  </Typography>
                </Stack>
              );
            })
          )}
          <Stack
            direction="row"
            spacing={1}
            sx={{ mt: 1, pt: 1, borderTop: '1px solid', borderColor: 'divider' }}
          >
            <Button size="small" onClick={() => setExcludedSources(new Set())}>
              All
            </Button>
            <Button
              size="small"
              onClick={() => setExcludedSources(new Set(sources.map((s) => s.source)))}
            >
              None
            </Button>
            <Button
              size="small"
              onClick={() => {
                setExcludedSources(new Set());
                localStorage.removeItem(STORAGE_KEYS.ACTIVITY_SOURCE_PREFS);
              }}
            >
              Reset
            </Button>
          </Stack>
        </Paper>
      )}
    </Box>
  );

  return (
    <Box sx={{ height: '100%', overflow: 'auto', p: 2 }}>
      {/* Header */}
      <Stack
        direction="row"
        spacing={2}
        sx={{
          alignItems: 'center',
          mb: 2,
        }}
      >
        <TimelineIcon />
        <Typography variant="h4" sx={{ flexGrow: 1 }}>
          Activity
        </Typography>
        {!isMobile && pausedByExpandRef.current && (
          <Chip
            label="Paused — row expanded"
            size="small"
            color="warning"
            variant="outlined"
            onDelete={() => {
              pausedByExpandRef.current = false;
              setAutoRefresh(true);
            }}
            deleteIcon={<span style={{ fontSize: '0.7rem', padding: '0 4px' }}>Follow log</span>}
          />
        )}
        {!isMobile && (
          <Button
            size="small"
            variant={autoRefresh ? 'contained' : 'outlined'}
            onClick={() => {
              pausedByExpandRef.current = false;
              setAutoRefresh(!autoRefresh);
            }}
          >
            {autoRefresh ? 'Auto-refresh ON' : 'Auto-refresh OFF'}
          </Button>
        )}
        <IconButton onClick={handleRefresh} title="Refresh">
          <RefreshIcon />
        </IconButton>
        {lastUpdated && (
          <Typography
            variant="caption"
            sx={{ color: 'text.secondary', ml: 1, alignSelf: 'center' }}
          >
            Updated {formatTimestamp(lastUpdated.toISOString())}
          </Typography>
        )}
      </Stack>

      {/* In-flight background file operations */}
      <PendingFileOpsBanner operations={pendingFileOps} />

      {/* Compact button outcome: the job was started (or refused), not finished */}
      <Snackbar
        open={compactNotice !== null}
        autoHideDuration={compactNotice?.severity === 'error' ? null : 8000}
        onClose={() => setCompactNotice(null)}
        anchorOrigin={{ vertical: 'bottom', horizontal: 'center' }}
      >
        <Alert
          severity={compactNotice?.severity ?? 'success'}
          onClose={() => setCompactNotice(null)}
          variant="filled"
          data-testid="compact-notice"
        >
          {compactNotice?.message ?? ''}
        </Alert>
      </Snackbar>

      {/* Pinned Operations Section */}
      {showOpsSection && (
        <Paper sx={{ p: 2, mb: 2 }}>
          <Stack
            direction="row"
            sx={{
              alignItems: 'center',
              justifyContent: 'space-between',
              mb: 1,
            }}
          >
            <Stack
              direction="row"
              spacing={1}
              sx={{
                alignItems: 'center',
              }}
            >
              {/* The count is liveOps, not opRows: the panel holds 24 hours
                  of history and read "Active Operations (91)" when every one of
                  the 91 had finished. The heading names the panel; the sections
                  below carry the outcome breakdown. */}
              <Typography variant="h6">
                Operations{liveOps.length > 0 ? ` — ${liveOps.length} active` : ''}
              </Typography>
              <Tooltip title={pinned ? 'Unpin section' : 'Pin section'}>
                <IconButton
                  size="small"
                  onClick={() => setPinned(!pinned)}
                  color={pinned ? 'primary' : 'default'}
                >
                  <PushPinIcon fontSize="small" />
                </IconButton>
              </Tooltip>
            </Stack>
            <Stack direction="row" spacing={1}>
              <Button
                size="small"
                variant="text"
                onClick={() => {
                  // Roll up every section, and every parent that has children.
                  setCollapsedSections(new Set(OPS_SECTION_KEYS));
                  setCollapsedParents(
                    new Set(
                      opRows
                        .filter((op) => op.parent_id && opRows.some((p) => p.id === op.parent_id))
                        .map((op) => op.parent_id as string)
                    )
                  );
                }}
              >
                Collapse All
              </Button>
              <Button
                size="small"
                variant="text"
                onClick={() => {
                  setCollapsedSections(new Set());
                  setCollapsedParents(new Set());
                }}
              >
                Expand All
              </Button>
              <Button size="small" variant="outlined" onClick={handleClearStale}>
                Clear Stale
              </Button>
            </Stack>
          </Stack>

          {/* error + no rows: the request failed and nothing is known — say so.
              error + rows:    stale — warn ABOVE the last confirmed list.
              no error + none: the server really has nothing.
              Before 2026-09-11 the first case rendered as the third. */}
          {opsLoadError && (
            <Alert
              severity={opRows.length === 0 ? 'error' : 'warning'}
              data-testid="activity-ops-load-error"
              sx={{ mb: opRows.length === 0 ? 0 : 1.5 }}
              action={
                <Button color="inherit" size="small" onClick={() => void loadActiveOpsFromServer()}>
                  Retry
                </Button>
              }
            >
              {opRows.length === 0
                ? `Could not load operations: ${opsLoadError}`
                : `Could not refresh operations — this list may be out of date: ${opsLoadError}`}
            </Alert>
          )}
          {opRows.length === 0 ? (
            !opsLoadError && (
              <Typography
                variant="body2"
                data-testid="activity-ops-empty"
                sx={{
                  color: 'text.secondary',
                }}
              >
                No active operations.
              </Typography>
            )
          ) : (
            <Stack spacing={1.5}>
              {/* Build hierarchical view: indent children by parent_id */}
              {(() => {
                // Create a map for quick parent lookup
                const opsById = Object.fromEntries(opRows.map((op) => [op.id, op]));

                // Count children per parent
                const childrenCount: Record<string, number> = {};
                for (const op of opRows) {
                  if (op.parent_id) {
                    childrenCount[op.parent_id] = (childrenCount[op.parent_id] ?? 0) + 1;
                  }
                }

                // Helper to get depth based on parent chain
                const getDepth = (op: (typeof opRows)[0]): number => {
                  let depth = 0;
                  let current = op;
                  while (current.parent_id && opsById[current.parent_id]) {
                    depth++;
                    current = opsById[current.parent_id];
                  }
                  return depth;
                };

                // Helper: is this op hidden because an ancestor is collapsed?
                // collapsedParents is seeded on first render (see useEffect above),
                // so size === 0 reliably means "user clicked Expand All".
                const isHiddenByCollapse = (op: (typeof opRows)[0]): boolean => {
                  let current = op;
                  while (current.parent_id && opsById[current.parent_id]) {
                    const parentId = current.parent_id;
                    if (collapsedParents.has(parentId)) return true;
                    current = opsById[parentId];
                  }
                  return false;
                };

                // Partition by OUTCOME, not into running/not-running. This
                // panel shows a 24-hour window, so most of what is in it has
                // finished; lumping every ended job into one "Completed"
                // section made a failed run and a successful one look alike,
                // and the header still counted all of them as active.
                //
                // isTerminal, not a status list. The previous list here named
                // interrupted_dropped and interrupted_restart but not
                // interrupted, interrupted_quiesced or interrupted_ask — the
                // backend mints one per ResumePolicy — so those three fell
                // through into Active and sat there forever. That is the exact
                // drift operationPolling.ts's comment warns about, and it is
                // why the panel read "Active Operations (91)" on a page whose
                // jobs had all ended.
                // Parent-collapse applies to EVERY section, history included.
                //
                // It used to apply to the live sections only, and the reason was
                // real: collapsing a `completed` parent would remove a `failed`
                // child from the Failed section and decrement its count, with
                // the row nowhere on the page. That hazard is gone rather than
                // managed — operationGrouping keys a group on def_id AND status,
                // so a group's children always share their parent's status and
                // therefore its section. A collapse can only ever hide rows from
                // the one section the parent is already visible in.
                //
                // It has to apply here, because every row in the report that
                // prompted this was TERMINAL. Filtering only the live sections
                // would give 13 rows where there were 12.
                const visibleOps = opRows.filter((op) => !isHiddenByCollapse(op));

                // History is newest-first; live work keeps its natural order so
                // rows do not jump around underneath a running progress bar.
                // finishedAt is undefined for anything the server sent before
                // fromV2 carried timestamps, and those sort last rather than
                // to the top.
                const newestFirst = (a: (typeof opRows)[0], b: (typeof opRows)[0]) =>
                  (b.finishedAt ?? b.startedAt ?? 0) - (a.finishedAt ?? a.startedAt ?? 0);

                // Order roots, then slot each parent's children directly
                // beneath it.
                //
                // A flat .sort() cannot be used once parents exist: a group's
                // members are minutes apart, so any unrelated op that finished
                // between two of them sorts BETWEEN a parent and its own
                // children, and the indentation then points at whatever row
                // happens to sit above. Sorting roots and expanding each into
                // its own subtree keeps the tree a tree whatever the timestamps
                // do. Recursive because depth is not this function's business
                // to assume.
                const withChildren = (
                  rows: typeof opRows,
                  compare?: (a: (typeof opRows)[0], b: (typeof opRows)[0]) => number
                ): typeof opRows => {
                  const kids = new Map<string, typeof opRows>();
                  const roots: typeof opRows = [];
                  for (const row of rows) {
                    const parentId = row.parent_id;
                    if (parentId && opsById[parentId]) {
                      const bucket = kids.get(parentId);
                      if (bucket) bucket.push(row);
                      else kids.set(parentId, [row]);
                    } else {
                      roots.push(row);
                    }
                  }
                  const emit = (level: typeof opRows): typeof opRows => {
                    const ordered = compare ? [...level].sort(compare) : level;
                    return ordered.flatMap((row) => [row, ...emit(kids.get(row.id) ?? [])]);
                  };
                  return emit(roots);
                };

                // A section heading is a CENSUS of operations, and a group row
                // is not an operation — it stands for several. Counting the
                // RENDERED rows would make the Failed heading read "Failed (1)"
                // for the twelve failed runs this page exists to surface, and
                // flip to "Failed (13)" the moment the group was expanded. A
                // number that changes when you click a disclosure triangle is
                // not a count of anything, and this page's whole problem is
                // rows that under-report what happened.
                //
                // So a section is ONE predicate applied twice: to the grouped
                // rows to decide what to render, and to the raw ungrouped set
                // to decide what the heading says. The two cannot drift,
                // because there is only one predicate.
                const sectionDefs: {
                  key: string;
                  title: string;
                  match: (op: (typeof opRows)[0]) => boolean;
                  // Omitted on the live sections: running work keeps its
                  // natural order so rows do not jump around underneath a
                  // progress bar. withChildren still runs there, to keep a
                  // group's members under their parent.
                  compare?: (a: (typeof opRows)[0], b: (typeof opRows)[0]) => number;
                }[] = [
                  {
                    key: 'active',
                    title: 'Active',
                    match: (o) => o.status !== 'queued' && !isTerminal(o.status),
                  },
                  { key: 'pending', title: 'Pending', match: (o) => o.status === 'queued' },
                  {
                    key: 'completed',
                    title: 'Completed',
                    match: (o) => o.status === 'completed',
                    compare: newestFirst,
                  },
                  {
                    key: 'failed',
                    title: 'Failed',
                    match: (o) => o.status === 'failed',
                    compare: newestFirst,
                  },
                  {
                    key: 'canceled',
                    title: 'Canceled',
                    match: (o) => o.status === 'canceled',
                    compare: newestFirst,
                  },
                  {
                    key: 'interrupted',
                    title: 'Interrupted',
                    // The whole interrupted_* family, by prefix, so a policy
                    // added on the backend lands here instead of in Active.
                    match: (o) => o.status === 'interrupted' || o.status.startsWith('interrupted_'),
                    compare: newestFirst,
                  },
                ];

                const sections = sectionDefs.map((def) => ({
                  key: def.key,
                  title: def.title,
                  ops: withChildren(visibleOps.filter(def.match), def.compare),
                  // rawOps, not visibleOps and not opRows: collapsing a group
                  // must not change the number, and a synthetic parent must
                  // never be counted as one more run than really happened.
                  count: rawOps.filter(def.match).length,
                }));

                const renderOp = (op: (typeof opRows)[0]) => {
                  // 2-decimal precision so a 49915-book scan shows 1.10 → 1.11
                  // → 1.12 instead of being welded to "1%" for hundreds of
                  // books at a time. fmtPct returns "1.10" not "1.1".
                  const pctNum = op.total > 0 ? (op.progress / op.total) * 100 : 0;
                  const pct = pctNum >= 100 ? '100' : pctNum.toFixed(2);
                  const pctBar = Math.min(100, pctNum); // for LinearProgress value
                  const depth = getDepth(op);
                  const indent = depth * 24; // 24px per level for indentation
                  const hasChildren = (childrenCount[op.id] ?? 0) > 0;
                  const effectiveCollapsed = collapsedParents.has(op.id);
                  // A synthetic group row. Its id is derived from its members,
                  // so the server has never heard of it: getOperationLogs,
                  // cancelOperation and the per-op refresh would all 404 on it.
                  // Everything keyed on the id is suppressed below, and the row
                  // click toggles the group instead of expanding a log panel.
                  const group = op.group;
                  const toggleCollapsed = () =>
                    setCollapsedParents((prev) => {
                      const next = new Set(prev);
                      if (next.has(op.id)) next.delete(op.id);
                      else next.add(op.id);
                      return next;
                    });

                  return (
                    <Paper
                      key={op.id}
                      variant="outlined"
                      sx={[
                        {
                          p: 1.5,
                          cursor: 'pointer',
                          ml: indent,
                          transition: 'all 0.2s ease',
                        },
                        expandedOpId === op.id
                          ? {
                              border: 2,
                            }
                          : {
                              border: 1,
                            },
                        expandedOpId === op.id
                          ? {
                              borderColor: 'primary.main',
                            }
                          : {
                              borderColor: 'divider',
                            },
                      ]}
                      onClick={() =>
                        group
                          ? toggleCollapsed()
                          : setExpandedOpId(expandedOpId === op.id ? null : op.id)
                      }
                    >
                      <Stack
                        direction="row"
                        sx={{
                          justifyContent: 'space-between',
                          alignItems: 'center',
                          mb: 0.5,
                        }}
                      >
                        <Stack
                          direction="row"
                          spacing={1}
                          sx={{
                            alignItems: 'center',
                          }}
                        >
                          {depth > 0 && (
                            <Typography
                              variant="caption"
                              sx={{ color: 'text.disabled', fontSize: '0.75rem', minWidth: 8 }}
                            >
                              ↳
                            </Typography>
                          )}
                          {hasChildren && (
                            <Typography
                              variant="caption"
                              sx={{
                                cursor: 'pointer',
                                color: 'text.secondary',
                                fontSize: '0.85rem',
                                userSelect: 'none',
                              }}
                              onClick={(e) => {
                                e.stopPropagation();
                                toggleCollapsed();
                              }}
                            >
                              {effectiveCollapsed ? '▸' : '▾'}
                            </Typography>
                          )}
                          <Typography
                            variant="subtitle2"
                            sx={{
                              fontWeight: 'bold',
                            }}
                          >
                            {operationDisplayName(op)}
                          </Typography>
                          {group && (
                            // What the row stands for, stated as a count. The
                            // status chip beside it is the members' status —
                            // they are homogeneous by construction — so "12
                            // runs" + "failed" reads as "12 runs, all failed".
                            <Chip
                              size="small"
                              variant="outlined"
                              label={`${group.count} runs`}
                              sx={{ fontVariantNumeric: 'tabular-nums' }}
                            />
                          )}
                          <Chip
                            size="small"
                            label={op.status === 'queued' ? 'pending' : op.status}
                            color={
                              op.status === 'queued'
                                ? 'default'
                                : op.status === 'completed'
                                  ? 'success'
                                  : op.status === 'failed'
                                    ? 'error'
                                    : op.status === 'canceled'
                                      ? 'warning'
                                      : 'info'
                            }
                          />
                        </Stack>
                        <Stack
                          direction="row"
                          spacing={0.5}
                          sx={{
                            alignItems: 'center',
                          }}
                        >
                          {/* Every control below acts on op.id against the
                              server, so none of them exists on a group row —
                              its id is derived from its members and names no
                              record. Expand the group and use the child's. */}
                          {/* Refresh only while the op can still change. A
                              finished row is history: refreshing it answers
                              nothing, so it gets Retry (below) or, for a
                              completed run, no per-op action at all. */}
                          {!group && !isTerminal(op.status) && (
                            <Tooltip title="Refresh this operation">
                              <IconButton
                                size="small"
                                onClick={(e) => {
                                  e.stopPropagation();
                                  handleRefreshOp(op.id);
                                }}
                                aria-label="Refresh"
                              >
                                <RefreshIcon fontSize="small" />
                              </IconButton>
                            </Tooltip>
                          )}
                          {/* Failed, canceled and the interrupted_* family:
                              everything that ended without finishing. */}
                          {!group && isTerminal(op.status) && op.status !== 'completed' && (
                            <Tooltip title="Retry — requeue this operation">
                              <IconButton
                                size="small"
                                onClick={(e) => {
                                  e.stopPropagation();
                                  handleRetryOp(op.id);
                                }}
                                aria-label="Retry"
                              >
                                <ReplayIcon fontSize="small" />
                              </IconButton>
                            </Tooltip>
                          )}
                          {/* An interrupted op is one the server either may
                              resume on the next restart or has already
                              dropped. Discard deletes its record, which
                              covers both: it leaves the list and the resume
                              sweep for good. (It used to call cancel, which
                              the server refuses with 404 on an already-
                              dropped row — see handleDiscardOp.) */}
                          {!group &&
                            (op.status === 'interrupted' ||
                              op.status.startsWith('interrupted_')) && (
                              <Tooltip title="Discard — remove this run; it will never resume">
                                <IconButton
                                  size="small"
                                  onClick={(e) => {
                                    e.stopPropagation();
                                    handleDiscardOp(op.id);
                                  }}
                                  aria-label="Discard"
                                  disabled={discarding.has(op.id)}
                                >
                                  <DeleteSweepIcon fontSize="small" />
                                </IconButton>
                              </Tooltip>
                            )}
                          {!group && (
                            <Tooltip title="Copy op summary">
                              <IconButton
                                size="small"
                                onClick={(e) => {
                                  e.stopPropagation();
                                  handleCopyOp(op);
                                }}
                                aria-label="Copy"
                              >
                                <ContentCopyIcon fontSize="small" />
                              </IconButton>
                            </Tooltip>
                          )}
                          {/* isTerminal, not a three-status list: an op that
                              ended at one of the interrupted_* statuses is
                              finished, and offering Cancel on it hands the user
                              a button that cannot do anything. */}
                          {!group && !isTerminal(op.status) && (
                            <Button
                              size="small"
                              color="error"
                              variant="outlined"
                              startIcon={<CancelIcon />}
                              onClick={(e) => {
                                e.stopPropagation();
                                handleCancelOp(op.id);
                              }}
                              disabled={cancelling.has(op.id)}
                            >
                              {cancelling.has(op.id) ? 'Cancelling...' : 'Cancel'}
                            </Button>
                          )}
                        </Stack>
                      </Stack>
                      {op.status === 'queued' ? (
                        <Typography
                          variant="caption"
                          sx={{
                            color: 'text.secondary',
                            fontStyle: 'italic',
                          }}
                        >
                          Waiting to start…
                        </Typography>
                      ) : [
                          'completed',
                          'failed',
                          'canceled',
                          'interrupted_dropped',
                          'interrupted_restart',
                        ].includes(op.status) ? (
                        // Terminal ops: a static bar colored by outcome, no
                        // animation. (Pre-fix the indeterminate branch animated
                        // forever for completed ops without total counts.)
                        //
                        // With a denominator the bar shows the REAL fraction,
                        // not 100. It was hardcoded to 100 for every terminal
                        // op, so a row reading "0 / 4 (0.00%)" rendered a full
                        // green bar directly above its own caption saying
                        // nothing had been done. "Terminal" and "complete" are
                        // not the same claim: a run that ended having processed
                        // none of its four items ended, but it did not finish
                        // them. Without a denominator there is no fraction to
                        // show, so that branch below still fills the bar.
                        op.total > 0 ? (
                          <Box>
                            <LinearProgress
                              variant="determinate"
                              value={pctBar}
                              color={
                                op.status === 'completed'
                                  ? 'success'
                                  : op.status === 'failed'
                                    ? 'error'
                                    : 'warning'
                              }
                              sx={{ height: 6, borderRadius: 1, mb: 0.5 }}
                            />
                            <Typography
                              variant="caption"
                              sx={{
                                color: 'text.secondary',
                              }}
                            >
                              {op.progress.toLocaleString()} / {op.total.toLocaleString()} ({pct}%)
                            </Typography>
                          </Box>
                        ) : (
                          <LinearProgress
                            variant="determinate"
                            value={100}
                            color={
                              op.status === 'completed'
                                ? 'success'
                                : op.status === 'failed'
                                  ? 'error'
                                  : 'warning'
                            }
                            sx={{ height: 6, borderRadius: 1, mb: 0.5 }}
                          />
                        )
                      ) : op.total > 0 ? (
                        <Box>
                          <LinearProgress
                            variant="determinate"
                            value={pctBar}
                            sx={{ height: 6, borderRadius: 1, mb: 0.5 }}
                          />
                          <Typography
                            variant="caption"
                            sx={{
                              color: 'text.secondary',
                            }}
                          >
                            {op.progress.toLocaleString()} / {op.total.toLocaleString()} ({pct}%)
                          </Typography>
                        </Box>
                      ) : (
                        // Running op with no progress total: indeterminate
                        // animation is correct.
                        <LinearProgress sx={{ height: 6, borderRadius: 1, mb: 0.5 }} />
                      )}
                      <Typography
                        variant="caption"
                        noWrap
                        title={op.message}
                        sx={{
                          color: 'text.secondary',
                          display: 'block',
                        }}
                      >
                        {op.message}
                      </Typography>
                      {op.current_item && op.status === 'running' && (
                        <Tooltip title={op.current_item} placement="bottom-start">
                          <Typography
                            variant="caption"
                            noWrap
                            sx={{
                              color: 'text.disabled',
                              display: 'block',
                              fontStyle: 'italic',
                              fontSize: '0.75rem',
                            }}
                          >
                            {op.current_item}
                          </Typography>
                        </Tooltip>
                      )}
                      <Collapse in={expandedOpId === op.id}>
                        <Box
                          ref={expandedOpId === op.id ? opLogsRef : undefined}
                          sx={{
                            mt: 1,
                            maxHeight: 300,
                            overflowY: 'auto',
                            bgcolor: 'grey.900',
                            color: 'grey.300',
                            borderRadius: 1,
                            p: 1,
                            fontFamily: 'monospace',
                            fontSize: '0.75rem',
                            lineHeight: 1.4,
                          }}
                          onClick={(e) => e.stopPropagation()}
                        >
                          {!opLogsLoaded ? (
                            <Typography
                              variant="caption"
                              sx={{
                                color: 'grey.500',
                              }}
                            >
                              Loading logs...
                            </Typography>
                          ) : opLogs.length === 0 ? (
                            <Typography
                              variant="caption"
                              sx={{
                                color: 'grey.500',
                              }}
                            >
                              No logs recorded for this operation.
                            </Typography>
                          ) : (
                            opLogs.map((line, i) => (
                              <Box key={i} sx={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
                                {line}
                              </Box>
                            ))
                          )}
                        </Box>
                      </Collapse>
                    </Paper>
                  );
                };

                return sections
                  .filter((s) => s.ops.length > 0)
                  .map((section) => {
                    const sectionCollapsed = collapsedSections.has(section.key);
                    const totalPages = Math.ceil(section.ops.length / OPS_SECTION_PAGE_SIZE);
                    // Clamp here rather than in the setter — see sectionPages.
                    const page = Math.min(sectionPages[section.key] ?? 1, totalPages);
                    const start = (page - 1) * OPS_SECTION_PAGE_SIZE;
                    const pageOps = section.ops.slice(start, start + OPS_SECTION_PAGE_SIZE);
                    return (
                      <Box key={section.key} sx={{ mb: 1 }}>
                        <Stack
                          direction="row"
                          spacing={0.5}
                          onClick={() =>
                            setCollapsedSections((prev) => {
                              const next = new Set(prev);
                              if (!next.delete(section.key)) next.add(section.key);
                              return next;
                            })
                          }
                          sx={{
                            alignItems: 'center',
                            cursor: 'pointer',
                            mb: 0.5,
                            userSelect: 'none',
                            '&:hover': { color: 'text.primary' },
                          }}
                        >
                          {sectionCollapsed ? (
                            <ChevronRightIcon fontSize="small" sx={{ color: 'text.secondary' }} />
                          ) : (
                            <ExpandMoreIcon fontSize="small" sx={{ color: 'text.secondary' }} />
                          )}
                          <Typography
                            variant="overline"
                            sx={{ color: 'text.secondary', fontWeight: 600 }}
                          >
                            {/* section.count, not section.ops.length: the
                                heading answers "how many completed today?",
                                which is a number of OPERATIONS. Rows are
                                paginated and grouped; operations are not. */}
                            {section.title} ({section.count})
                          </Typography>
                        </Stack>
                        <Collapse in={!sectionCollapsed} unmountOnExit>
                          <Stack spacing={1}>{pageOps.map(renderOp)}</Stack>
                          {totalPages > 1 && (
                            <Stack
                              direction="row"
                              spacing={1}
                              sx={{ alignItems: 'center', justifyContent: 'center', pt: 1 }}
                            >
                              <Typography variant="caption" sx={{ color: 'text.secondary' }}>
                                {start + 1}–{start + pageOps.length} of {section.ops.length}
                              </Typography>
                              <Pagination
                                count={totalPages}
                                page={page}
                                onChange={(_, p) =>
                                  setSectionPages((prev) => ({ ...prev, [section.key]: p }))
                                }
                                color="primary"
                                size="small"
                              />
                            </Stack>
                          )}
                        </Collapse>
                      </Box>
                    );
                  });
              })()}
            </Stack>
          )}
        </Paper>
      )}

      {/* Compound Filter Bar */}
      <Paper sx={{ p: 2, mb: 2 }}>
        {isMobile ? (
          /* ---- Mobile layout ---- */
          <Stack spacing={1.5}>
            {/* Search always visible */}
            <TextField
              size="small"
              placeholder="Search summaries..."
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              fullWidth
            />

            {/* Toggle row */}
            <Stack
              direction="row"
              sx={{
                alignItems: 'center',
                justifyContent: 'space-between',
              }}
            >
              <Button
                size="small"
                variant="outlined"
                startIcon={<FilterListIcon />}
                onClick={() => setFiltersExpanded(!filtersExpanded)}
                endIcon={
                  activeFilterCount > 0 ? (
                    <Chip
                      size="small"
                      label={activeFilterCount}
                      color="primary"
                      sx={{ height: 18, fontSize: '0.65rem' }}
                    />
                  ) : undefined
                }
              >
                Filters
              </Button>
              <Stack
                direction="row"
                spacing={1}
                sx={{
                  alignItems: 'center',
                }}
              >
                <Typography
                  variant="caption"
                  sx={{
                    color: 'text.secondary',
                  }}
                >
                  {total} entries
                </Typography>
                {hasActiveFilters && (
                  <IconButton size="small" onClick={clearFilters} title="Clear filters">
                    <ClearIcon fontSize="small" />
                  </IconButton>
                )}
              </Stack>
            </Stack>

            {/* Collapsible filters */}
            <Collapse in={filtersExpanded}>
              <Stack spacing={1.5}>
                {/* Tier chips */}
                {tierChips}

                {/* Type dropdown */}
                <TextField
                  select
                  size="small"
                  label="Type"
                  value={typeFilter}
                  onChange={(e) => setTypeFilter(e.target.value)}
                  fullWidth
                >
                  <MenuItem value="">All Types</MenuItem>
                  {EVENT_TYPES.map((t) => (
                    <MenuItem key={t} value={t}>
                      {t.replace(/_/g, ' ')}
                    </MenuItem>
                  ))}
                </TextField>

                {/* Level dropdown */}
                <TextField
                  select
                  size="small"
                  label="Level"
                  value={levelFilter}
                  onChange={(e) => setLevelFilter(e.target.value)}
                  fullWidth
                >
                  <MenuItem value="">All Levels</MenuItem>
                  <MenuItem value="debug">debug</MenuItem>
                  <MenuItem value="info">info</MenuItem>
                  <MenuItem value="warn">warn</MenuItem>
                  <MenuItem value="error">error</MenuItem>
                </TextField>

                {/* Date range */}
                <TextField
                  size="small"
                  label="Since"
                  type={sinceFilter ? 'datetime-local' : 'text'}
                  placeholder="All time"
                  value={sinceFilter}
                  onFocus={(e) => {
                    if (!sinceFilter) (e.target as HTMLInputElement).type = 'datetime-local';
                  }}
                  onChange={(e) => setSinceFilter(e.target.value)}
                  helperText={
                    sinceFilter === defaultSince
                      ? `Default: last ${DEFAULT_SINCE_HOURS}h — clear for all history`
                      : undefined
                  }
                  fullWidth
                  slotProps={{
                    input: sinceFilter
                      ? {
                          endAdornment: (
                            <IconButton size="small" onClick={() => setSinceFilter('')}>
                              <ClearIcon fontSize="small" />
                            </IconButton>
                          ),
                        }
                      : undefined,

                    inputLabel: { shrink: true },
                  }}
                />

                <TextField
                  size="small"
                  label="Until"
                  type={untilFilter ? 'datetime-local' : 'text'}
                  placeholder="Now"
                  value={untilFilter}
                  onFocus={(e) => {
                    if (!untilFilter) (e.target as HTMLInputElement).type = 'datetime-local';
                  }}
                  onChange={(e) => setUntilFilter(e.target.value)}
                  fullWidth
                  slotProps={{
                    input: untilFilter
                      ? {
                          endAdornment: (
                            <IconButton size="small" onClick={() => setUntilFilter('')}>
                              <ClearIcon fontSize="small" />
                            </IconButton>
                          ),
                        }
                      : undefined,

                    inputLabel: { shrink: true },
                  }}
                />

                {/* Sources */}
                {sourcesButton(true)}

                {/* Tag filter chips */}
                <Box>
                  <Typography variant="caption" sx={{ display: 'block', mb: 0.5, fontWeight: 500 }}>
                    Outcome
                  </Typography>
                  <Stack
                    direction="row"
                    spacing={0.5}
                    sx={{
                      flexWrap: 'wrap',
                    }}
                  >
                    {['outcome:ok', 'outcome:warn', 'outcome:error', 'outcome:skip'].map((tag) => {
                      const { color, sx, label } = tagChipProps(tag);
                      return (
                        <Chip
                          key={tag}
                          label={label}
                          size="small"
                          color={color}
                          sx={[
                            {
                              cursor: 'pointer',
                            },
                            sx ?? {},
                          ]}
                          variant={tagFilter.includes(tag) ? 'filled' : 'outlined'}
                          clickable
                          onClick={() => toggleTagFilter(tag)}
                        />
                      );
                    })}
                  </Stack>
                </Box>

                <Box>
                  <Typography variant="caption" sx={{ display: 'block', mb: 0.5, fontWeight: 500 }}>
                    Action
                  </Typography>
                  <Stack
                    direction="row"
                    spacing={0.5}
                    sx={{
                      flexWrap: 'wrap',
                    }}
                  >
                    {[
                      'action:metadata-apply',
                      'action:tag-write',
                      'action:import',
                      'action:scan',
                      'action:dedup',
                      'action:fingerprint',
                      'action:fingerprint-scan',
                      'action:organizer',
                      'action:purge',
                    ].map((tag) => {
                      const { color, sx, label } = tagChipProps(tag);
                      return (
                        <Chip
                          key={tag}
                          label={label}
                          size="small"
                          color={color}
                          sx={[
                            {
                              cursor: 'pointer',
                            },
                            sx ?? {},
                          ]}
                          variant={tagFilter.includes(tag) ? 'filled' : 'outlined'}
                          clickable
                          onClick={() => toggleTagFilter(tag)}
                        />
                      );
                    })}
                  </Stack>
                </Box>

                {/* Compact button */}
                <Button
                  size="small"
                  variant="outlined"
                  disabled={compacting}
                  onClick={(e) => setCompactAnchor(e.currentTarget)}
                  fullWidth
                >
                  {compacting ? 'Compacting…' : 'Compact'}
                </Button>
                <Menu
                  anchorEl={compactAnchor}
                  open={Boolean(compactAnchor)}
                  onClose={() => {
                    setCompactAnchor(null);
                    setCustomCompactDays('');
                  }}
                >
                  <MenuItem onClick={() => handleCompact(0)}>Everything (now)</MenuItem>
                  {[3, 7, 14, 30, 60].map((days) => (
                    <MenuItem key={days} onClick={() => handleCompact(days)}>
                      Older than {days} days
                    </MenuItem>
                  ))}
                  <MenuItem disableRipple sx={{ '&:hover': { bgcolor: 'transparent' } }}>
                    <TextField
                      size="small"
                      type="number"
                      placeholder="Custom days"
                      value={customCompactDays}
                      onChange={(e) => setCustomCompactDays(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') {
                          const n = parseInt(customCompactDays, 10);
                          if (n > 0) handleCompact(n);
                        }
                        e.stopPropagation();
                      }}
                      onClick={(e) => e.stopPropagation()}
                      sx={{ width: 120 }}
                      slotProps={{
                        input: { inputProps: { min: 0 } },
                      }}
                    />
                  </MenuItem>
                </Menu>
                {/* Auto-refresh (moved here on mobile) */}
                <Button
                  size="small"
                  variant={autoRefresh ? 'contained' : 'outlined'}
                  onClick={() => setAutoRefresh(!autoRefresh)}
                  fullWidth
                >
                  {autoRefresh ? 'Auto-refresh ON' : 'Auto-refresh OFF'}
                </Button>
              </Stack>
            </Collapse>
          </Stack>
        ) : (
          /* ---- Desktop layout (unchanged) ---- */
          <Stack spacing={1.5}>
            {/* Row 1: Search + tier chips */}
            <Stack
              direction="row"
              spacing={2}
              sx={{
                alignItems: 'center',
                flexWrap: 'wrap',
              }}
            >
              <TextField
                size="small"
                placeholder="Search summaries..."
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                sx={{ minWidth: 220 }}
              />
              {tierChips}
            </Stack>

            {/* Row 2: Type, Level, dates, sources */}
            <Stack
              direction="row"
              spacing={2}
              sx={{
                alignItems: 'center',
                flexWrap: 'wrap',
              }}
            >
              <TextField
                select
                size="small"
                label="Type"
                value={typeFilter}
                onChange={(e) => setTypeFilter(e.target.value)}
                sx={{ minWidth: 180 }}
              >
                <MenuItem value="">All Types</MenuItem>
                {EVENT_TYPES.map((t) => (
                  <MenuItem key={t} value={t}>
                    {t.replace(/_/g, ' ')}
                  </MenuItem>
                ))}
              </TextField>

              <TextField
                select
                size="small"
                label="Level"
                value={levelFilter}
                onChange={(e) => setLevelFilter(e.target.value)}
                sx={{ minWidth: 140 }}
              >
                <MenuItem value="">All Levels</MenuItem>
                <MenuItem value="debug">debug</MenuItem>
                <MenuItem value="info">info</MenuItem>
                <MenuItem value="warn">warn</MenuItem>
                <MenuItem value="error">error</MenuItem>
              </TextField>

              <TextField
                size="small"
                label="Since"
                type={sinceFilter ? 'datetime-local' : 'text'}
                placeholder="All time"
                value={sinceFilter}
                onFocus={(e) => {
                  if (!sinceFilter) (e.target as HTMLInputElement).type = 'datetime-local';
                }}
                onChange={(e) => setSinceFilter(e.target.value)}
                helperText={
                  sinceFilter === defaultSince
                    ? `Default: last ${DEFAULT_SINCE_HOURS}h — clear for all history`
                    : undefined
                }
                sx={{ minWidth: 180 }}
                slotProps={{
                  input: sinceFilter
                    ? {
                        endAdornment: (
                          <IconButton size="small" onClick={() => setSinceFilter('')}>
                            <ClearIcon fontSize="small" />
                          </IconButton>
                        ),
                      }
                    : undefined,

                  inputLabel: { shrink: true },
                }}
              />

              <TextField
                size="small"
                label="Until"
                type={untilFilter ? 'datetime-local' : 'text'}
                placeholder="Now"
                value={untilFilter}
                onFocus={(e) => {
                  if (!untilFilter) (e.target as HTMLInputElement).type = 'datetime-local';
                }}
                onChange={(e) => setUntilFilter(e.target.value)}
                sx={{ minWidth: 180 }}
                slotProps={{
                  input: untilFilter
                    ? {
                        endAdornment: (
                          <IconButton size="small" onClick={() => setUntilFilter('')}>
                            <ClearIcon fontSize="small" />
                          </IconButton>
                        ),
                      }
                    : undefined,

                  inputLabel: { shrink: true },
                }}
              />

              {/* Sources dropdown */}
              {sourcesButton()}

              {/* Compact button */}
              <Button
                size="small"
                variant="outlined"
                disabled={compacting}
                onClick={(e) => setCompactAnchor(e.currentTarget)}
              >
                {compacting ? 'Compacting…' : 'Compact'}
              </Button>
              <Menu
                anchorEl={compactAnchor}
                open={Boolean(compactAnchor)}
                onClose={() => {
                  setCompactAnchor(null);
                  setCustomCompactDays('');
                }}
              >
                <MenuItem onClick={() => handleCompact(0)}>Everything (now)</MenuItem>
                {[3, 7, 14, 30, 60].map((days) => (
                  <MenuItem key={days} onClick={() => handleCompact(days)}>
                    Older than {days} days
                  </MenuItem>
                ))}
                <MenuItem disableRipple sx={{ '&:hover': { bgcolor: 'transparent' } }}>
                  <TextField
                    size="small"
                    type="number"
                    placeholder="Custom days"
                    value={customCompactDays}
                    onChange={(e) => setCustomCompactDays(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') {
                        const n = parseInt(customCompactDays, 10);
                        if (n > 0) handleCompact(n);
                      }
                      e.stopPropagation();
                    }}
                    onClick={(e) => e.stopPropagation()}
                    sx={{ width: 120 }}
                    slotProps={{
                      input: { inputProps: { min: 0 } },
                    }}
                  />
                </MenuItem>
              </Menu>
            </Stack>
            {/* Row 3: Active filter summary */}
            <Stack
              direction="row"
              spacing={1}
              sx={{
                alignItems: 'center',
              }}
            >
              <Typography
                variant="caption"
                sx={{
                  color: 'text.secondary',
                }}
              >
                {total} entries
              </Typography>
              {hasActiveFilters && (
                <Button size="small" startIcon={<ClearIcon />} onClick={clearFilters}>
                  Clear filters
                </Button>
              )}
            </Stack>
          </Stack>
        )}
      </Paper>
      {/* Large-log warning banner */}
      {total > 1000 && (
        <Typography
          variant="body2"
          sx={{
            mb: 1,
            p: 1,
            bgcolor: 'warning.light',
            borderRadius: 1,
            color: 'warning.contrastText',
          }}
        >
          Showing most recent {pageSize} of {total.toLocaleString()} entries. Use filters or compact
          old entries to reduce log size.
        </Typography>
      )}

      {/* Source-count failures are advisory: the feed itself may be fine, so
          this is a separate, dismissable banner rather than a page-level error. */}
      {sourcesError && (
        <Alert
          severity="warning"
          data-testid="activity-sources-error"
          sx={{ mb: 1 }}
          onClose={() => setSourcesError(null)}
        >
          Source counts unavailable — the Sources filter may be incomplete. {sourcesError}
        </Alert>
      )}

      {/* Activity Feed */}
      <Paper sx={{ position: 'relative' }}>
        {/* Unobtrusive top-edge indicator for background refreshes */}
        {refreshing && (
          <LinearProgress
            sx={{ position: 'absolute', top: 0, left: 0, right: 0, borderRadius: '4px 4px 0 0' }}
          />
        )}
        {/* Four DISTINGUISHABLE states, in priority order:
              loading  — request outstanding, nothing to show yet
              error    — request failed and there is no usable data
              empty    — request succeeded and the log really is empty
              table    — data (optionally with a stale-data warning on top)
            Before this, all four collapsed into "No activity entries found." */}
        {loading ? (
          <Box
            data-testid="activity-loading"
            sx={{ display: 'flex', justifyContent: 'center', py: 6 }}
          >
            <CircularProgress />
          </Box>
        ) : error && entries.length === 0 ? (
          <Box sx={{ p: 3 }}>
            <Alert
              severity="error"
              data-testid="activity-error"
              action={
                <Button color="inherit" size="small" onClick={handleRefresh}>
                  Retry
                </Button>
              }
            >
              <AlertTitle>Could not load activity</AlertTitle>
              {error}
            </Alert>
          </Box>
        ) : entries.length === 0 ? (
          <Box data-testid="activity-empty" sx={{ py: 4, textAlign: 'center' }}>
            <Typography
              variant="body2"
              sx={{
                color: 'text.secondary',
              }}
            >
              {operationId
                ? 'No activity entries for this operation (pre-migration).'
                : sinceFilter
                  ? 'No activity entries in the selected time range.'
                  : 'No activity entries found.'}
            </Typography>
            {/* The default 24h window must never look like an empty log — give
                the user the one-click way out of it. */}
            {sinceFilter && !operationId && (
              <Button size="small" sx={{ mt: 1 }} onClick={() => setSinceFilter('')}>
                Search all history
              </Button>
            )}
          </Box>
        ) : (
          <>
            {/* Stale data: the last refresh failed but the previously loaded
                page is still on screen. Warning, not error — the rows are real,
                just not current. */}
            {error && (
              <Alert severity="warning" data-testid="activity-stale-error" sx={{ m: 1 }}>
                Showing the last successful result — the most recent refresh failed. {error}
              </Alert>
            )}
            <TableContainer>
              <Table size="small">
                <TableHead>
                  <TableRow>
                    <TableCell>Time</TableCell>
                    <TableCell>Level</TableCell>
                    <TableCell>Type</TableCell>
                    <TableCell sx={{ width: '40%' }}>Summary</TableCell>
                    {!isMobile && <TableCell>Source</TableCell>}
                    {!isMobile && <TableCell>Tags</TableCell>}
                    <TableCell />
                  </TableRow>
                </TableHead>
                <TableBody>
                  {entries.map((entry) => {
                    // Batched entries: collapsed/expanded list view
                    if ((entry.details as any)?.batched === true) {
                      return (
                        <BatchActivityEntry
                          key={entry.id}
                          entry={entry}
                          tierColor={TIER_COLORS[entry.tier] ?? '#757575'}
                        />
                      );
                    }
                    if (entry.tier === 'digest') {
                      const isExpanded = expandedDigests.has(String(entry.id));
                      const details = entry.details as
                        | {
                            date?: string;
                            original_count?: number;
                            counts?: Record<string, number>;
                            tag_counts?: Record<string, Record<string, number>>;
                            items?: Array<{
                              type: string;
                              tier?: string;
                              book?: string;
                              book_id?: string;
                              operation_id?: string;
                              summary: string;
                              details?: string;
                              timestamp?: string;
                              tags?: string[];
                            }>;
                            truncated?: boolean;
                            truncated_count?: number;
                          }
                        | undefined;
                      // Pre-2026-05-20 digests won't have per-item timestamps or tags
                      // because the source rows were already destroyed before this
                      // field was added. Detect by checking the first item's timestamp.
                      const isLegacyDigest = (() => {
                        if (!details?.date) return false;
                        const cutoff = new Date('2026-05-20');
                        const digestDate = new Date(details.date);
                        return digestDate < cutoff && !details?.items?.[0]?.timestamp;
                      })();
                      const rawCounts = details?.counts || {};
                      // Fall back to tag_counts.action when Counts is sparse (only
                      // the single legacy "system_log" key) so old digests show a
                      // meaningful breakdown rather than one undifferentiated chip.
                      const countsKeys = Object.keys(rawCounts);
                      const isLegacySparse =
                        countsKeys.length === 1 && countsKeys[0] === 'system_log';
                      const counts: Record<string, number> = isLegacySparse
                        ? (details?.tag_counts?.action ?? rawCounts)
                        : rawCounts;
                      const items = details?.items || [];

                      return (
                        <React.Fragment key={entry.id}>
                          <TableRow
                            hover
                            sx={{ bgcolor: 'rgba(0, 137, 123, 0.06)', cursor: 'pointer' }}
                            onClick={() => {
                              setExpandedDigests((prev) => {
                                const next = new Set(prev);
                                const key = String(entry.id);
                                if (next.has(key)) next.delete(key);
                                else next.add(key);
                                return next;
                              });
                            }}
                          >
                            <TableCell
                              sx={{
                                whiteSpace: 'nowrap',
                                color: 'text.secondary',
                                fontSize: '0.75rem',
                              }}
                            >
                              {details?.date || entry.timestamp}
                            </TableCell>
                            <TableCell>
                              <Chip
                                size="small"
                                label="digest"
                                sx={{ bgcolor: '#00897b', color: 'white' }}
                              />
                            </TableCell>
                            <TableCell>
                              <Stack
                                direction="row"
                                spacing={0.5}
                                sx={{
                                  flexWrap: 'wrap',
                                }}
                              >
                                {Object.entries(counts)
                                  .slice(0, 6)
                                  .map(([type, count]) => (
                                    <Chip
                                      key={type}
                                      size="small"
                                      variant="outlined"
                                      label={`${count} ${type.replace(/_/g, ' ')}`}
                                    />
                                  ))}
                              </Stack>
                            </TableCell>
                            <TableCell colSpan={isMobile ? 1 : 2}>
                              <Typography variant="body2">
                                {entry.summary} {isExpanded ? '▾' : '▸'}
                              </Typography>
                            </TableCell>
                            {!isMobile && <TableCell />}
                            <TableCell />
                          </TableRow>
                          {isExpanded && (
                            <TableRow>
                              <TableCell colSpan={isMobile ? 5 : 7} sx={{ py: 0, px: 2 }}>
                                <Box sx={{ maxHeight: 400, overflow: 'auto', py: 1 }}>
                                  {isLegacyDigest && (
                                    <Typography
                                      variant="caption"
                                      sx={{
                                        color: 'text.secondary',
                                        display: 'block',
                                        mb: 1,
                                        fontStyle: 'italic',
                                      }}
                                    >
                                      Pre-2026-05-20 digest — per-item timestamps and tags
                                      unavailable (source rows already compacted away)
                                    </Typography>
                                  )}
                                  {items.map((item, idx) => (
                                    <Stack
                                      key={idx}
                                      direction="row"
                                      spacing={1}
                                      sx={[
                                        {
                                          alignItems: 'center',
                                          flexWrap: 'wrap',
                                        },
                                        {
                                          py: 0.5,
                                          borderBottom: '1px solid',
                                          borderColor: 'divider',
                                        },
                                        item.type === 'error'
                                          ? {
                                              color: 'error.main',
                                            }
                                          : {
                                              color: 'text.primary',
                                            },
                                      ]}
                                    >
                                      {item.timestamp && (
                                        <Typography
                                          variant="caption"
                                          sx={{
                                            color: 'text.secondary',
                                            fontFamily: 'monospace',
                                            minWidth: 70,
                                          }}
                                        >
                                          {formatItemTime(item.timestamp)}
                                        </Typography>
                                      )}
                                      <Chip
                                        size="small"
                                        label={item.type.replace(/_/g, ' ')}
                                        sx={{ minWidth: 100 }}
                                      />
                                      {item.tier === 'audit' && (
                                        <Chip
                                          size="small"
                                          label="audit"
                                          sx={{
                                            bgcolor: '#7c4dff',
                                            color: 'white',
                                            fontSize: '0.65rem',
                                          }}
                                        />
                                      )}
                                      {item.book_id ? (
                                        <Typography
                                          variant="body2"
                                          component="span"
                                          sx={{
                                            cursor: 'pointer',
                                            color: 'primary.main',
                                            fontWeight: 500,
                                          }}
                                          onClick={(e: React.MouseEvent) => {
                                            e.stopPropagation();
                                            navigate(`/library/${item.book_id}`);
                                          }}
                                        >
                                          {item.book || item.book_id}
                                        </Typography>
                                      ) : (
                                        <Typography
                                          variant="body2"
                                          component="span"
                                          sx={{ fontWeight: 500 }}
                                        >
                                          {item.book || '—'}
                                        </Typography>
                                      )}
                                      <Typography
                                        variant="body2"
                                        sx={{
                                          color: 'text.secondary',
                                          flex: 1,
                                        }}
                                      >
                                        {item.summary}
                                      </Typography>
                                      {item.operation_id && (
                                        <Chip
                                          size="small"
                                          label={item.operation_id.slice(0, 12)}
                                          title={`op:${item.operation_id} — click to filter`}
                                          color="info"
                                          variant="outlined"
                                          sx={{
                                            cursor: 'pointer',
                                            fontFamily: 'monospace',
                                            fontSize: '0.65rem',
                                          }}
                                          clickable
                                          onClick={(e: React.MouseEvent) => {
                                            e.stopPropagation();
                                            toggleTagFilter(`op:${item.operation_id}`);
                                          }}
                                        />
                                      )}
                                      {item.details && (
                                        <Typography
                                          variant="caption"
                                          sx={{
                                            color: 'error.main',
                                          }}
                                        >
                                          {item.details}
                                        </Typography>
                                      )}
                                      {displayTags(item.tags).length > 0 && (
                                        <Stack
                                          direction="row"
                                          spacing={0.5}
                                          sx={{
                                            flexWrap: 'wrap',
                                          }}
                                        >
                                          {displayTags(item.tags).map((tag) => {
                                            const { color, sx: tagSx, label } = tagChipProps(tag);
                                            return (
                                              <Chip
                                                key={tag}
                                                size="small"
                                                label={label}
                                                color={color}
                                                sx={[
                                                  {
                                                    cursor: 'pointer',
                                                    fontSize: '0.65rem',
                                                  },
                                                  tagSx ?? {},
                                                ]}
                                                variant={
                                                  tagFilter.includes(tag) ? 'filled' : 'outlined'
                                                }
                                                clickable
                                                onClick={(e: React.MouseEvent) => {
                                                  e.stopPropagation();
                                                  toggleTagFilter(tag);
                                                }}
                                              />
                                            );
                                          })}
                                        </Stack>
                                      )}
                                    </Stack>
                                  ))}
                                  {details?.truncated && (
                                    <Typography
                                      variant="caption"
                                      sx={{
                                        color: 'text.secondary',
                                        pt: 1,
                                        display: 'block',
                                      }}
                                    >
                                      … and {details.truncated_count?.toLocaleString()} more entries
                                      not shown
                                    </Typography>
                                  )}
                                </Box>
                              </TableCell>
                            </TableRow>
                          )}
                        </React.Fragment>
                      );
                    }

                    // Regular entry
                    return (
                      <TableRow
                        key={entry.id}
                        hover
                        sx={[
                          {
                            bgcolor: rowBgColor(entry),
                          },
                          entry.tier === 'debug'
                            ? {
                                opacity: 0.6,
                              }
                            : {
                                opacity: 1,
                              },
                        ]}
                      >
                        <TableCell
                          sx={{
                            whiteSpace: 'nowrap',
                            color: 'text.secondary',
                            fontSize: '0.75rem',
                          }}
                        >
                          {isMobile
                            ? formatTimestampCompact(entry.timestamp)
                            : formatTimestamp(entry.timestamp)}
                        </TableCell>
                        <TableCell>{levelChip(entry.level)}</TableCell>
                        <TableCell>
                          <Chip size="small" label={(entry.type || '').replace(/_/g, ' ')} />
                        </TableCell>
                        <TableCell
                          sx={
                            isMobile ? { wordBreak: 'break-word', minWidth: 0 } : { maxWidth: 400 }
                          }
                        >
                          <Typography variant="body2" noWrap={!isMobile} title={entry.summary}>
                            {entry.summary}
                          </Typography>
                          {entry.operation_id && !operationId && (
                            <Typography
                              variant="caption"
                              sx={{ cursor: 'pointer', color: 'primary.main' }}
                              onClick={() => setOperationId(entry.operation_id!)}
                            >
                              view operation &rarr;
                            </Typography>
                          )}
                          {entry.book_id && (
                            <Typography
                              variant="caption"
                              sx={{ cursor: 'pointer', color: 'primary.main', ml: 1 }}
                              onClick={() => navigate(`/library/${entry.book_id}`)}
                            >
                              book &rarr;
                            </Typography>
                          )}
                        </TableCell>
                        {!isMobile && (
                          <TableCell>
                            <Typography
                              variant="caption"
                              sx={{
                                color: 'text.secondary',
                              }}
                            >
                              {entry.source}
                            </Typography>
                          </TableCell>
                        )}
                        {!isMobile && (
                          <TableCell>
                            {displayTags(entry.tags).length > 0 ? (
                              <Stack
                                direction="row"
                                spacing={0.5}
                                sx={{
                                  flexWrap: 'wrap',
                                }}
                              >
                                {displayTags(entry.tags).map((tag) => {
                                  const { color, sx, label } = tagChipProps(tag);
                                  return (
                                    <Chip
                                      key={tag}
                                      size="small"
                                      label={label}
                                      color={color}
                                      sx={[
                                        {
                                          cursor: 'pointer',
                                        },
                                        sx ?? {},
                                      ]}
                                      variant={tagFilter.includes(tag) ? 'filled' : 'outlined'}
                                      clickable
                                      onClick={(e) => {
                                        e.stopPropagation();
                                        toggleTagFilter(tag);
                                      }}
                                    />
                                  );
                                })}
                              </Stack>
                            ) : null}
                          </TableCell>
                        )}
                        <TableCell>
                          {entry.operation_id &&
                            (entry.type === 'organize_completed' ||
                              entry.type === 'metadata_applied') && (
                              <Tooltip title="Revert operation">
                                <IconButton size="small" onClick={() => void openRevert(entry)}>
                                  <UndoIcon fontSize="small" />
                                </IconButton>
                              </Tooltip>
                            )}
                        </TableCell>
                      </TableRow>
                    );
                  })}
                </TableBody>
              </Table>
            </TableContainer>
          </>
        )}

        <Stack
          direction="row"
          spacing={2}
          sx={{
            justifyContent: 'center',
            alignItems: 'center',
            py: 2,
          }}
        >
          {totalPages > 1 && (
            <Pagination
              count={totalPages}
              page={page}
              onChange={(_, p) => setPage(p)}
              color="primary"
              size={isMobile ? 'small' : 'medium'}
            />
          )}
          <TextField
            select
            size="small"
            value={pageSize}
            onChange={(e) => {
              setPageSize(Number(e.target.value));
              setPage(1);
            }}
            sx={{ minWidth: 90 }}
          >
            {PAGE_SIZE_OPTIONS.map((n) => (
              <MenuItem key={n} value={n}>
                {n} / page
              </MenuItem>
            ))}
          </TextField>
        </Stack>
      </Paper>

      {/* Revert Confirmation Dialog */}
      <Dialog open={!!revertEntry} onClose={() => setRevertEntry(null)}>
        <DialogTitle>Revert Operation?</DialogTitle>
        <DialogContent>
          <Typography variant="body2" data-testid="revert-plan">
            {revertPlan}
          </Typography>
          <Typography variant="body2" sx={{ mt: 1 }}>
            Operation <strong>{revertEntry?.operation_id?.slice(0, 12)}...</strong>. This cannot be undone.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setRevertEntry(null)}>Cancel</Button>
          <Button color="warning" variant="contained" onClick={handleRevert} disabled={reverting}>
            {reverting ? 'Reverting...' : 'Revert'}
          </Button>
        </DialogActions>
      </Dialog>

      <Snackbar
        open={toast !== null}
        autoHideDuration={6000}
        onClose={() => setToast(null)}
        message={toast ?? ''}
        anchorOrigin={{ vertical: 'bottom', horizontal: 'center' }}
      />
    </Box>
  );
}
