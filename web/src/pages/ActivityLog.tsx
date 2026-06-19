// file: web/src/pages/ActivityLog.tsx
// version: 2.17.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-06-17

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import {
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
  Stack,
  Table,
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
import RefreshIcon from '@mui/icons-material/Refresh.js';
import ContentCopyIcon from '@mui/icons-material/ContentCopy.js';
import PushPinIcon from '@mui/icons-material/PushPin.js';
import TimelineIcon from '@mui/icons-material/Timeline.js';
import ClearIcon from '@mui/icons-material/Clear.js';
import UndoIcon from '@mui/icons-material/Undo.js';
import CancelIcon from '@mui/icons-material/Cancel.js';
import FilterListIcon from '@mui/icons-material/FilterList.js';
import { fetchActivity, fetchActivitySources, compactActivityLog } from '../services/activityApi';
import type { ActivityEntry, SourceCount } from '../services/activityApi';
import { BatchActivityEntry } from '../components/BatchActivityEntry';
import * as api from '../services/api';
import { PendingFileOpsBanner } from '../components/PendingFileOpsBanner';
import { usePendingFileOps } from '../hooks/usePendingFileOps';
import { useOperationsStore } from '../stores/useOperationsStore';
import { STORAGE_KEYS } from '../lib/storageKeys';
import { tagChipProps } from '../utils/activityTagColors';

const PAGE_SIZE_OPTIONS = [25, 50, 100, 250];

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
    <Chip
      size="small"
      label={level}
      color={colorMap[level] ?? 'default'}
      variant="outlined"
    />
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
  const [sinceFilter, setSinceFilter] = useState('');
  const [untilFilter, setUntilFilter] = useState('');
  const [excludedSources, setExcludedSources] = useState<Set<string>>(() => {
    const saved = localStorage.getItem(STORAGE_KEYS.ACTIVITY_SOURCE_PREFS);
    return saved ? new Set(JSON.parse(saved)) : new Set();
  });
  const [hideNoOp, setHideNoOp] = useState(true);
  const [tagFilter, setTagFilter] = useState<string[]>([]);

  /** Toggle a single tag in the tag filter. Adds if absent, removes if present. */
  const toggleTagFilter = useCallback((tag: string) => {
    setTagFilter((prev) =>
      prev.includes(tag) ? prev.filter((t) => t !== tag) : [...prev, tag]
    );
  }, []);

  // Mobile filter collapse
  const [filtersExpanded, setFiltersExpanded] = useState(false);

  // Pending background file operations (cover embed, tag write, rename)
  const { operations: pendingFileOps } = usePendingFileOps();

  // Active ops from unified store
  const activeOps = useOperationsStore((state) => state.activeOperations);
  const loadActiveOpsFromServer = useOperationsStore((state) => state.loadFromServer);
  const latestLogEvent = useOperationsStore((state) => state.latestLogEvent);
  const [pinned, setPinned] = useState(() => localStorage.getItem(STORAGE_KEYS.ACTIVITY_OPS_PINNED) !== 'false');
  const [cancelling, setCancelling] = useState<Set<string>>(new Set());
  const [expandedOpId, setExpandedOpId] = useState<string | null>(searchParams.get('op'));
  // pausedByExpand: true when auto-refresh was auto-paused because a row is
  // expanded. Cleared when the row collapses or the user clicks "Follow log".
  const pausedByExpandRef = useRef(false);
  const [opLogs, setOpLogs] = useState<string[]>([]);
  // opLogsLoaded distinguishes "haven't fetched yet" from "fetched, empty".
  // Without this, an op with zero logs shows "Loading logs..." forever.
  const [opLogsLoaded, setOpLogsLoaded] = useState(false);

  // Tree collapse state: set of parent op IDs that are collapsed.
  // Seeded on first render with ops: parents with ≥3 children start collapsed.
  // After seeding, collapsedParents.size > 0 means "some parents collapsed",
  // and new Set() unambiguously means "all expanded" (not "use defaults").
  const [collapsedParents, setCollapsedParents] = useState<Set<string>>(new Set());
  const collapsedInitializedRef = useRef(false);
  const opLogsRef = useRef<HTMLDivElement>(null);

  // Sources
  const [sources, setSources] = useState<SourceCount[]>([]);
  const [sourcesOpen, setSourcesOpen] = useState(false);

  // Feed
  const [entries, setEntries] = useState<ActivityEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(25);
  const [loading, setLoading] = useState(false);
  // Silent background refresh — updates data without destroying the table DOM
  const [refreshing, setRefreshing] = useState(false);

  // Auto-refresh
  const [autoRefresh, setAutoRefresh] = useState(true);
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);

  // Compact
  const [compactAnchor, setCompactAnchor] = useState<null | HTMLElement>(null);
  const [compacting, setCompacting] = useState(false);
  const [customCompactDays, setCustomCompactDays] = useState('');
  const [expandedDigests, setExpandedDigests] = useState<Set<string>>(new Set());

  // Revert dialog
  const [revertEntry, setRevertEntry] = useState<ActivityEntry | null>(null);
  const [reverting, setReverting] = useState(false);

  // Refs for intervals
  const opsIntervalRef = useRef<number | null>(null);
  const feedIntervalRef = useRef<number | null>(null);
  const sourcesDropdownRef = useRef<HTMLDivElement | null>(null);

  // Tracks pending scroll timers so they can be cancelled on unmount — a timer
  // that fires after unmount would touch a detached ref (harmless here thanks to
  // optional chaining, but still flagged by the memory-leak scanner).
  const scrollTimeoutsRef = useRef<number[]>([]);
  useEffect(
    () => () => {
      scrollTimeoutsRef.current.forEach((id) => window.clearTimeout(id));
      scrollTimeoutsRef.current = [];
    },
    [],
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

  // Seed collapsedParents with default-collapsed parents (≥3 children) on
  // first render that has ops. Uses a ref guard so user interactions after
  // initial seed are not overwritten. This makes "Expand All" work correctly:
  // setCollapsedParents(new Set()) = size 0 = all expanded (no fallback needed).
  useEffect(() => {
    if (collapsedInitializedRef.current || activeOps.length === 0) return;
    const childrenCount: Record<string, number> = {};
    for (const op of activeOps) {
      if (op.parent_id) {
        childrenCount[op.parent_id] = (childrenCount[op.parent_id] ?? 0) + 1;
      }
    }
    const defaults = new Set<string>();
    for (const [id, count] of Object.entries(childrenCount)) {
      if (count >= 3) defaults.add(id);
    }
    if (defaults.size > 0) {
      collapsedInitializedRef.current = true;
      setCollapsedParents(defaults);
    }
  }, [activeOps]);

  const loadOperationLogs = useCallback(async (opId: string) => {
    const logs = await api.getOperationLogs(opId);
    setOpLogs(logs.map((l: { message?: string }) => l.message || String(l)));
    setOpLogsLoaded(true);
    const scrollTimeout = window.setTimeout(
      () => opLogsRef.current?.scrollTo({ top: opLogsRef.current.scrollHeight }),
      50,
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
    setOpLogs((prev) => [...prev, latestLogEvent.message]);
    const scrollTimeout = window.setTimeout(
      () => opLogsRef.current?.scrollTo({ top: opLogsRef.current.scrollHeight }),
      50,
    );
    scrollTimeoutsRef.current.push(scrollTimeout);
  }, [latestLogEvent, expandedOpId]);

  // Load sources
  const loadSources = useCallback(async () => {
    try {
      const data = await fetchActivitySources({
        since: sinceFilter || undefined,
        until: untilFilter || undefined,
      });
      setSources(data.sources || []);
    } catch (err) {
      console.error('Failed to load sources', err);
    }
  }, [sinceFilter, untilFilter]);

  // Load activity feed. Pass silent=true for background refreshes to avoid
  // replacing the table with a spinner (which resets scroll position).
  const loadFeed = useCallback(async (p: number, silent = false) => {
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

      const result = await fetchActivity({
        limit: pageSize,
        offset: (p - 1) * pageSize,
        type: typeFilter || undefined,
        level: levelFilter || undefined,
        operation_id: operationId.trim() || undefined,
        since: sinceFilter || undefined,
        until: untilFilter || undefined,
        search: search.trim() || undefined,
        exclude_sources: excludeStr,
        exclude_tiers: excludeTiersStr,
        exclude_tags: hideNoOp ? 'no-op' : undefined,
        tags: tagFilter.length > 0 ? tagFilter.join(',') : undefined,
      });

      setEntries(result.entries || []);
      setTotal(result.total || 0);
    } catch (err) {
      console.error('Failed to load activity', err);
      setEntries([]);
      setTotal(0);
    } finally {
      setLoading(false);
      setRefreshing(false);
      setLastUpdated(new Date());
    }
  }, [typeFilter, levelFilter, operationId, sinceFilter, untilFilter, search, excludedSources, tiers, hideNoOp, tagFilter]);

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

  // Load feed when filters change
  useEffect(() => {
    setPage(1);
    loadFeed(1);
    loadSources();
  }, [typeFilter, levelFilter, operationId, sinceFilter, untilFilter, search, excludedSources, tiers, hideNoOp, tagFilter, loadFeed, loadSources]);

  // Load feed on page or pageSize change
  useEffect(() => {
    loadFeed(page);
  }, [page, pageSize, loadFeed]);

  // Auto-refresh feed — 5s when active ops exist, 30s when idle.
  // Uses silent=true so the table stays in the DOM and scroll position is preserved.
  const refreshInterval = activeOps.length > 0 ? 5000 : 30000;
  useEffect(() => {
    if (feedIntervalRef.current) window.clearInterval(feedIntervalRef.current);
    if (autoRefresh) {
      feedIntervalRef.current = window.setInterval(() => {
        loadFeed(page, true);
        loadSources();
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

  const handleCancelOp = async (opId: string) => {
    setCancelling((prev) => new Set(prev).add(opId));
    try {
      await api.cancelOperation(opId);
      await loadActiveOpsFromServer();
    } catch (err) {
      console.error('Failed to cancel operation', err);
    }
    setCancelling((prev) => {
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
  // has Auto-refresh OFF but wants the truth on one op.
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

  // Per-op copy: plain-text summary suitable for pasting into a bug report
  // or sharing with another claude session — id, def, status, progress,
  // message, timestamps.
  const handleCopyOp = async (op: typeof activeOps[0]) => {
    const lines = [
      `id:       ${op.id}`,
      `def:      ${op.def_id ?? op.type}`,
      `name:     ${op.displayName ?? ''}`,
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

  const handleRevert = async () => {
    if (!revertEntry?.operation_id) return;
    setReverting(true);
    try {
      await api.revertOperation(revertEntry.operation_id);
      loadFeed(page);
    } catch (err) {
      console.error('Failed to revert operation', err);
    } finally {
      setReverting(false);
      setRevertEntry(null);
    }
  };

  const handleCompact = async (days: number) => {
    setCompactAnchor(null);
    setCompacting(true);
    try {
      const result = await compactActivityLog(days);
      alert(`Compacted ${result.days_compacted} days, removed ${result.entries_deleted.toLocaleString()} entries`);
      loadFeed(page);
    } catch (err) {
      alert(`Compaction failed: ${err}`);
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
    sinceFilter !== '' ||
    untilFilter !== '' ||
    excludedSources.size > 0 ||
    !hideNoOp;

  // Count active non-search filters (for mobile badge)
  const activeFilterCount = [
    tiers.size !== 3 || !tiers.has('audit') || !tiers.has('change') || !tiers.has('digest'),
    typeFilter !== '',
    levelFilter !== '',
    operationId !== '',
    sinceFilter !== '',
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
    setSinceFilter('');
    setUntilFilter('');
    setExcludedSources(new Set());
    setHideNoOp(true);
  };

  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const showOpsSection = pinned || activeOps.length > 0;

  // Shared filter controls (used in both mobile collapsed and desktop layouts)
  const tierChips = (
    <Stack direction="row" spacing={1} flexWrap="wrap">
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
        sx={{
          borderWidth: hideNoOp ? 2 : 1,
          cursor: 'pointer',
          opacity: hideNoOp ? 1 : 0.6,
        }}
      />
    </Stack>
  );

  const sourcesButton = (fullWidth?: boolean) => (
    <Box sx={{ position: 'relative', width: fullWidth ? '100%' : undefined }} ref={sourcesDropdownRef}>
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
            <Typography variant="body2" color="text.secondary" sx={{ p: 1 }}>
              No sources found.
            </Typography>
          ) : (
            sources.map((s) => {
              const isExcluded = excludedSources.has(s.source);
              return (
                <Stack
                  key={s.source}
                  direction="row"
                  alignItems="center"
                  spacing={1}
                  sx={{
                    px: 1,
                    py: 0.5,
                    cursor: 'pointer',
                    '&:hover': { bgcolor: 'action.hover' },
                  }}
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
                >
                  <input type="checkbox" checked={!isExcluded} readOnly style={{ pointerEvents: 'none' }} />
                  <Typography
                    variant="body2"
                    sx={{
                      textDecoration: isExcluded ? 'line-through' : 'none',
                      opacity: isExcluded ? 0.5 : 1,
                      flexGrow: 1,
                    }}
                  >
                    {s.source}
                  </Typography>
                  <Typography variant="caption" color="text.secondary">
                    {s.count}
                  </Typography>
                </Stack>
              );
            })
          )}
          <Stack direction="row" spacing={1} sx={{ mt: 1, pt: 1, borderTop: '1px solid', borderColor: 'divider' }}>
            <Button size="small" onClick={() => setExcludedSources(new Set())}>All</Button>
            <Button size="small" onClick={() => setExcludedSources(new Set(sources.map((s) => s.source)))}>None</Button>
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
      <Stack direction="row" alignItems="center" spacing={2} sx={{ mb: 2 }}>
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
          <Typography variant="caption" sx={{ color: 'text.secondary', ml: 1, alignSelf: 'center' }}>
            Updated {formatTimestamp(lastUpdated.toISOString())}
          </Typography>
        )}
      </Stack>

      {/* In-flight background file operations */}
      <PendingFileOpsBanner operations={pendingFileOps} />

      {/* Pinned Operations Section */}
      {showOpsSection && (
        <Paper sx={{ p: 2, mb: 2 }}>
          <Stack direction="row" alignItems="center" justifyContent="space-between" sx={{ mb: 1 }}>
            <Stack direction="row" alignItems="center" spacing={1}>
              <Typography variant="h6">
                Active Operations ({activeOps.length})
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
                  // Collapse all parents that have children
                  const parents = new Set(
                    activeOps
                      .filter((op) => op.parent_id && activeOps.some((p) => p.id === op.parent_id))
                      .map((op) => op.parent_id as string)
                  );
                  setCollapsedParents(parents);
                }}
              >
                Collapse All
              </Button>
              <Button
                size="small"
                variant="text"
                onClick={() => setCollapsedParents(new Set())}
              >
                Expand All
              </Button>
              <Button size="small" variant="outlined" onClick={handleClearStale}>
                Clear Stale
              </Button>
            </Stack>
          </Stack>

          {activeOps.length === 0 ? (
            <Typography variant="body2" color="text.secondary">
              No active operations.
            </Typography>
          ) : (
            <Stack spacing={1.5}>
              {/* Build hierarchical view: indent children by parent_id */}
              {(() => {
                // Create a map for quick parent lookup
                const opsById = Object.fromEntries(activeOps.map((op) => [op.id, op]));

                // Count children per parent
                const childrenCount: Record<string, number> = {};
                for (const op of activeOps) {
                  if (op.parent_id) {
                    childrenCount[op.parent_id] = (childrenCount[op.parent_id] ?? 0) + 1;
                  }
                }

                // Helper to get depth based on parent chain
                const getDepth = (op: typeof activeOps[0]): number => {
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
                const isHiddenByCollapse = (op: typeof activeOps[0]): boolean => {
                  let current = op;
                  while (current.parent_id && opsById[current.parent_id]) {
                    const parentId = current.parent_id;
                    if (collapsedParents.has(parentId)) return true;
                    current = opsById[parentId];
                  }
                  return false;
                };

                // Partition by status so finished jobs aren't mixed with
                // running ones. Within each section the existing
                // parent-child hierarchy still works.
                const TERMINAL_STATUSES = ['completed', 'failed', 'canceled', 'interrupted_dropped', 'interrupted_restart'];
                const visibleOps = activeOps.filter((op) => !isHiddenByCollapse(op));
                const sections: { key: string; title: string; ops: typeof activeOps }[] = [
                  { key: 'pending',   title: 'Pending',   ops: visibleOps.filter((o) => o.status === 'queued') },
                  { key: 'active',    title: 'Active',    ops: visibleOps.filter((o) => o.status !== 'queued' && !TERMINAL_STATUSES.includes(o.status)) },
                  { key: 'completed', title: 'Completed', ops: visibleOps.filter((o) => TERMINAL_STATUSES.includes(o.status)) },
                ];

                const renderOp = (op: typeof activeOps[0]) => {
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

                  return (
                    <Paper
                      key={op.id}
                      variant="outlined"
                      sx={{
                        p: 1.5,
                        cursor: 'pointer',
                        border: expandedOpId === op.id ? 2 : 1,
                        borderColor: expandedOpId === op.id ? 'primary.main' : 'divider',
                        ml: indent,
                        transition: 'all 0.2s ease',
                      }}
                      onClick={() => setExpandedOpId(expandedOpId === op.id ? null : op.id)}
                    >
                      <Stack direction="row" justifyContent="space-between" alignItems="center" sx={{ mb: 0.5 }}>
                        <Stack direction="row" spacing={1} alignItems="center">
                          {depth > 0 && (
                            <Typography variant="caption" sx={{ color: 'text.disabled', fontSize: '0.75rem', minWidth: 8 }}>
                              ↳
                            </Typography>
                          )}
                          {hasChildren && (
                            <Typography
                              variant="caption"
                              sx={{ cursor: 'pointer', color: 'text.secondary', fontSize: '0.85rem', userSelect: 'none' }}
                              onClick={(e) => {
                                e.stopPropagation();
                                setCollapsedParents((prev) => {
                                  const next = new Set(prev);
                                  if (next.has(op.id)) next.delete(op.id);
                                  else next.add(op.id);
                                  return next;
                                });
                              }}
                            >
                              {effectiveCollapsed ? '▸' : '▾'}
                            </Typography>
                          )}
                          <Typography variant="subtitle2" fontWeight="bold">
                            {op.displayName || op.def_id || op.type.replace(/_/g, ' ')}
                          </Typography>
                          <Chip
                            size="small"
                            label={op.status === 'queued' ? 'pending' : op.status}
                            color={
                              op.status === 'queued' ? 'default' :
                              op.status === 'completed' ? 'success' :
                              op.status === 'failed' ? 'error' :
                              op.status === 'canceled' ? 'warning' :
                              'info'
                            }
                          />
                        </Stack>
                        <Stack direction="row" spacing={0.5} alignItems="center">
                          <Tooltip title="Refresh this operation">
                            <IconButton
                              size="small"
                              onClick={(e) => { e.stopPropagation(); handleRefreshOp(op.id); }}
                              aria-label="Refresh"
                            >
                              <RefreshIcon fontSize="small" />
                            </IconButton>
                          </Tooltip>
                          <Tooltip title="Copy op summary">
                            <IconButton
                              size="small"
                              onClick={(e) => { e.stopPropagation(); handleCopyOp(op); }}
                              aria-label="Copy"
                            >
                              <ContentCopyIcon fontSize="small" />
                            </IconButton>
                          </Tooltip>
                          {!['completed', 'failed', 'canceled'].includes(op.status) && (
                            <Button
                              size="small"
                              color="error"
                              variant="outlined"
                              startIcon={<CancelIcon />}
                              onClick={(e) => { e.stopPropagation(); handleCancelOp(op.id); }}
                              disabled={cancelling.has(op.id)}
                            >
                              {cancelling.has(op.id) ? 'Cancelling...' : 'Cancel'}
                            </Button>
                          )}
                        </Stack>
                      </Stack>
                      {op.status === 'queued' ? (
                        <Typography variant="caption" color="text.secondary" sx={{ fontStyle: 'italic' }}>
                          Waiting to start…
                        </Typography>
                      ) : ['completed', 'failed', 'canceled', 'interrupted_dropped', 'interrupted_restart'].includes(op.status) ? (
                        // Terminal ops: show a static full bar colored by
                        // outcome. No animation. (Pre-fix the indeterminate
                        // branch animated forever for completed ops without
                        // total counts.)
                        op.total > 0 ? (
                          <Box>
                            <LinearProgress
                              variant="determinate"
                              value={100}
                              color={op.status === 'completed' ? 'success' : op.status === 'failed' ? 'error' : 'warning'}
                              sx={{ height: 6, borderRadius: 1, mb: 0.5 }}
                            />
                            <Typography variant="caption" color="text.secondary">
                              {op.progress.toLocaleString()} / {op.total.toLocaleString()} ({pct}%)
                            </Typography>
                          </Box>
                        ) : (
                          <LinearProgress
                            variant="determinate"
                            value={100}
                            color={op.status === 'completed' ? 'success' : op.status === 'failed' ? 'error' : 'warning'}
                            sx={{ height: 6, borderRadius: 1, mb: 0.5 }}
                          />
                        )
                      ) : op.total > 0 ? (
                        <Box>
                          <LinearProgress variant="determinate" value={pctBar} sx={{ height: 6, borderRadius: 1, mb: 0.5 }} />
                          <Typography variant="caption" color="text.secondary">
                            {op.progress.toLocaleString()} / {op.total.toLocaleString()} ({pct}%)
                          </Typography>
                        </Box>
                      ) : (
                        // Running op with no progress total: indeterminate
                        // animation is correct.
                        <LinearProgress sx={{ height: 6, borderRadius: 1, mb: 0.5 }} />
                      )}
                      <Typography variant="caption" color="text.secondary" display="block" noWrap title={op.message}>
                        {op.message}
                      </Typography>
                      {op.current_item && op.status === 'running' && (
                        <Tooltip title={op.current_item} placement="bottom-start">
                          <Typography variant="caption" color="text.disabled" display="block" noWrap
                            sx={{ fontStyle: 'italic', fontSize: '0.75rem' }}>
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
                            <Typography variant="caption" color="grey.500">Loading logs...</Typography>
                          ) : opLogs.length === 0 ? (
                            <Typography variant="caption" color="grey.500">No logs recorded for this operation.</Typography>
                          ) : (
                            opLogs.map((line, i) => (
                              <Box key={i} sx={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{line}</Box>
                            ))
                          )}
                        </Box>
                      </Collapse>
                    </Paper>
                  );
                };

                return sections
                  .filter((s) => s.ops.length > 0)
                  .map((section) => (
                    <Box key={section.key} sx={{ mb: 1 }}>
                      <Typography
                        variant="overline"
                        sx={{ color: 'text.secondary', fontWeight: 600, display: 'block', mb: 0.5 }}
                      >
                        {section.title} ({section.ops.length})
                      </Typography>
                      <Stack spacing={1}>{section.ops.map(renderOp)}</Stack>
                    </Box>
                  ));
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
            <Stack direction="row" alignItems="center" justifyContent="space-between">
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
              <Stack direction="row" alignItems="center" spacing={1}>
                <Typography variant="caption" color="text.secondary">
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
                  onFocus={(e) => { if (!sinceFilter) (e.target as HTMLInputElement).type = 'datetime-local'; }}
                  onChange={(e) => setSinceFilter(e.target.value)}
                  InputLabelProps={{ shrink: true }}
                  InputProps={sinceFilter ? {
                    endAdornment: <IconButton size="small" onClick={() => setSinceFilter('')}><ClearIcon fontSize="small" /></IconButton>,
                  } : undefined}
                  fullWidth
                />

                <TextField
                  size="small"
                  label="Until"
                  type={untilFilter ? 'datetime-local' : 'text'}
                  placeholder="Now"
                  value={untilFilter}
                  onFocus={(e) => { if (!untilFilter) (e.target as HTMLInputElement).type = 'datetime-local'; }}
                  onChange={(e) => setUntilFilter(e.target.value)}
                  InputLabelProps={{ shrink: true }}
                  InputProps={untilFilter ? {
                    endAdornment: <IconButton size="small" onClick={() => setUntilFilter('')}><ClearIcon fontSize="small" /></IconButton>,
                  } : undefined}
                  fullWidth
                />

                {/* Sources */}
                {sourcesButton(true)}

                {/* Tag filter chips */}
                <Box>
                  <Typography variant="caption" sx={{ display: 'block', mb: 0.5, fontWeight: 500 }}>
                    Outcome
                  </Typography>
                  <Stack direction="row" spacing={0.5} flexWrap="wrap">
                    {['outcome:ok', 'outcome:warn', 'outcome:error', 'outcome:skip'].map((tag) => {
                      const { color, sx, label } = tagChipProps(tag);
                      return (
                        <Chip
                          key={tag}
                          label={label}
                          size="small"
                          color={color}
                          sx={{ cursor: 'pointer', ...(sx ?? {}) }}
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
                  <Stack direction="row" spacing={0.5} flexWrap="wrap">
                    {['action:metadata-apply', 'action:tag-write', 'action:import', 'action:scan', 'action:dedup', 'action:fingerprint', 'action:fingerprint-scan', 'action:organizer', 'action:purge'].map((tag) => {
                      const { color, sx, label } = tagChipProps(tag);
                      return (
                        <Chip
                          key={tag}
                          label={label}
                          size="small"
                          color={color}
                          sx={{ cursor: 'pointer', ...(sx ?? {}) }}
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
                  onClose={() => { setCompactAnchor(null); setCustomCompactDays(''); }}
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
                      InputProps={{ inputProps: { min: 0 } }}
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
            <Stack direction="row" spacing={2} alignItems="center" flexWrap="wrap">
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
            <Stack direction="row" spacing={2} alignItems="center" flexWrap="wrap">
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
                onFocus={(e) => { if (!sinceFilter) (e.target as HTMLInputElement).type = 'datetime-local'; }}
                onChange={(e) => setSinceFilter(e.target.value)}
                InputLabelProps={{ shrink: true }}
                InputProps={sinceFilter ? {
                  endAdornment: <IconButton size="small" onClick={() => setSinceFilter('')}><ClearIcon fontSize="small" /></IconButton>,
                } : undefined}
                sx={{ minWidth: 180 }}
              />

              <TextField
                size="small"
                label="Until"
                type={untilFilter ? 'datetime-local' : 'text'}
                placeholder="Now"
                value={untilFilter}
                onFocus={(e) => { if (!untilFilter) (e.target as HTMLInputElement).type = 'datetime-local'; }}
                onChange={(e) => setUntilFilter(e.target.value)}
                InputLabelProps={{ shrink: true }}
                InputProps={untilFilter ? {
                  endAdornment: <IconButton size="small" onClick={() => setUntilFilter('')}><ClearIcon fontSize="small" /></IconButton>,
                } : undefined}
                sx={{ minWidth: 180 }}
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
                onClose={() => { setCompactAnchor(null); setCustomCompactDays(''); }}
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
                    InputProps={{ inputProps: { min: 0 } }}
                  />
                </MenuItem>
              </Menu>
            </Stack>

            {/* Row 3: Active filter summary */}
            <Stack direction="row" spacing={1} alignItems="center">
              <Typography variant="caption" color="text.secondary">
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
          Showing most recent {pageSize} of {total.toLocaleString()} entries. Use filters or compact old entries to reduce log size.
        </Typography>
      )}

      {/* Activity Feed */}
      <Paper sx={{ position: 'relative' }}>
        {/* Unobtrusive top-edge indicator for background refreshes */}
        {refreshing && (
          <LinearProgress sx={{ position: 'absolute', top: 0, left: 0, right: 0, borderRadius: '4px 4px 0 0' }} />
        )}
        {loading ? (
          <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}>
            <CircularProgress />
          </Box>
        ) : entries.length === 0 ? (
          <Typography
            variant="body2"
            color="text.secondary"
            sx={{ py: 4, textAlign: 'center' }}
          >
            {operationId
              ? 'No activity entries for this operation (pre-migration).'
              : 'No activity entries found.'}
          </Typography>
        ) : (
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
                  const details = entry.details as {
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
                  } | undefined;
                  // Pre-2026-05-20 digests won't have per-item timestamps or tags
                  // because the source rows were already destroyed before this
                  // field was added. Detect by checking the first item's timestamp.
                  const isLegacyDigest = (() => {
                    if (!details?.date) return false;
                    const cutoff = new Date('2026-05-20');
                    const digestDate = new Date(details.date);
                    return digestDate < cutoff && !(details?.items?.[0]?.timestamp);
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
                        <TableCell sx={{ whiteSpace: 'nowrap', color: 'text.secondary', fontSize: '0.75rem' }}>
                          {details?.date || entry.timestamp}
                        </TableCell>
                        <TableCell>
                          <Chip size="small" label="digest" sx={{ bgcolor: '#00897b', color: 'white' }} />
                        </TableCell>
                        <TableCell>
                          <Stack direction="row" spacing={0.5} flexWrap="wrap">
                            {Object.entries(counts).slice(0, 6).map(([type, count]) => (
                              <Chip key={type} size="small" variant="outlined" label={`${count} ${type.replace(/_/g, ' ')}`} />
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
                                  color="text.secondary"
                                  sx={{ display: 'block', mb: 1, fontStyle: 'italic' }}
                                >
                                  Pre-2026-05-20 digest — per-item timestamps and tags unavailable (source rows already compacted away)
                                </Typography>
                              )}
                              {items.map((item, idx) => (
                                <Stack
                                  key={idx}
                                  direction="row"
                                  spacing={1}
                                  alignItems="center"
                                  flexWrap="wrap"
                                  sx={{
                                    py: 0.5,
                                    borderBottom: '1px solid',
                                    borderColor: 'divider',
                                    color: item.type === 'error' ? 'error.main' : 'text.primary',
                                  }}
                                >
                                  {item.timestamp && (
                                    <Typography variant="caption" color="text.secondary" sx={{ fontFamily: 'monospace', minWidth: 70 }}>
                                      {formatItemTime(item.timestamp)}
                                    </Typography>
                                  )}
                                  <Chip size="small" label={item.type.replace(/_/g, ' ')} sx={{ minWidth: 100 }} />
                                  {item.tier === 'audit' && (
                                    <Chip size="small" label="audit" sx={{ bgcolor: '#7c4dff', color: 'white', fontSize: '0.65rem' }} />
                                  )}
                                  {item.book_id ? (
                                    <Typography
                                      variant="body2"
                                      component="span"
                                      sx={{ cursor: 'pointer', color: 'primary.main', fontWeight: 500 }}
                                      onClick={(e: React.MouseEvent) => { e.stopPropagation(); navigate(`/library/${item.book_id}`); }}
                                    >
                                      {item.book || item.book_id}
                                    </Typography>
                                  ) : (
                                    <Typography variant="body2" component="span" sx={{ fontWeight: 500 }}>
                                      {item.book || '—'}
                                    </Typography>
                                  )}
                                  <Typography variant="body2" color="text.secondary" sx={{ flex: 1 }}>
                                    {item.summary}
                                  </Typography>
                                  {item.operation_id && (
                                    <Chip
                                      size="small"
                                      label={item.operation_id.slice(0, 12)}
                                      title={`op:${item.operation_id} — click to filter`}
                                      color="info"
                                      variant="outlined"
                                      sx={{ cursor: 'pointer', fontFamily: 'monospace', fontSize: '0.65rem' }}
                                      clickable
                                      onClick={(e: React.MouseEvent) => {
                                        e.stopPropagation();
                                        toggleTagFilter(`op:${item.operation_id}`);
                                      }}
                                    />
                                  )}
                                  {item.details && (
                                    <Typography variant="caption" color="error.main">
                                      {item.details}
                                    </Typography>
                                  )}
                                  {displayTags(item.tags).length > 0 && (
                                    <Stack direction="row" spacing={0.5} flexWrap="wrap">
                                      {displayTags(item.tags).map((tag) => {
                                        const { color, sx: tagSx, label } = tagChipProps(tag);
                                        return (
                                          <Chip
                                            key={tag}
                                            size="small"
                                            label={label}
                                            color={color}
                                            sx={{ cursor: 'pointer', fontSize: '0.65rem', ...(tagSx ?? {}) }}
                                            variant={tagFilter.includes(tag) ? 'filled' : 'outlined'}
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
                                <Typography variant="caption" color="text.secondary" sx={{ pt: 1, display: 'block' }}>
                                  … and {details.truncated_count?.toLocaleString()} more entries not shown
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
                    sx={{
                      bgcolor: rowBgColor(entry),
                      opacity: entry.tier === 'debug' ? 0.6 : 1,
                    }}
                  >
                    <TableCell sx={{ whiteSpace: 'nowrap', color: 'text.secondary', fontSize: '0.75rem' }}>
                      {isMobile ? formatTimestampCompact(entry.timestamp) : formatTimestamp(entry.timestamp)}
                    </TableCell>
                    <TableCell>{levelChip(entry.level)}</TableCell>
                    <TableCell>
                      <Chip size="small" label={(entry.type || '').replace(/_/g, ' ')} />
                    </TableCell>
                    <TableCell sx={isMobile ? { wordBreak: 'break-word', minWidth: 0 } : { maxWidth: 400 }}>
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
                        <Typography variant="caption" color="text.secondary">
                          {entry.source}
                        </Typography>
                      </TableCell>
                    )}
                    {!isMobile && (
                      <TableCell>
                        {displayTags(entry.tags).length > 0 ? (
                          <Stack direction="row" spacing={0.5} flexWrap="wrap">
                            {displayTags(entry.tags).map((tag) => {
                              const { color, sx, label } = tagChipProps(tag);
                              return (
                                <Chip
                                  key={tag}
                                  size="small"
                                  label={label}
                                  color={color}
                                  sx={{ cursor: 'pointer', ...(sx ?? {}) }}
                                  variant={tagFilter.includes(tag) ? 'filled' : 'outlined'}
                                  clickable
                                  onClick={(e) => { e.stopPropagation(); toggleTagFilter(tag); }}
                                />
                              );
                            })}
                          </Stack>
                        ) : null}
                      </TableCell>
                    )}
                    <TableCell>
                      {entry.operation_id &&
                        (entry.type === 'organize_completed' || entry.type === 'metadata_applied') && (
                          <Tooltip title="Revert operation">
                            <IconButton
                              size="small"
                              onClick={() => setRevertEntry(entry)}
                            >
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
        )}

        <Stack direction="row" justifyContent="center" alignItems="center" spacing={2} sx={{ py: 2 }}>
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
            onChange={(e) => { setPageSize(Number(e.target.value)); setPage(1); }}
            sx={{ minWidth: 90 }}
          >
            {PAGE_SIZE_OPTIONS.map((n) => (
              <MenuItem key={n} value={n}>{n} / page</MenuItem>
            ))}
          </TextField>
        </Stack>
      </Paper>

      {/* Revert Confirmation Dialog */}
      <Dialog open={!!revertEntry} onClose={() => setRevertEntry(null)}>
        <DialogTitle>Revert Operation?</DialogTitle>
        <DialogContent>
          <Typography variant="body2">
            This will undo all tracked changes from operation{' '}
            <strong>{revertEntry?.operation_id?.slice(0, 12)}...</strong>.
            This cannot be undone.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setRevertEntry(null)}>Cancel</Button>
          <Button
            color="warning"
            variant="contained"
            onClick={handleRevert}
            disabled={reverting}
          >
            {reverting ? 'Reverting...' : 'Revert'}
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
