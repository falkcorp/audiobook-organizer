// file: core/web/packages/shell/src/layout/OperationsIndicatorFrame.tsx
// version: 0.1.0
// guid: 1b7e4d92-5c36-4a08-9f1d-c2e8a0b6d374
// last-edited: 2026-11-21

// The audiobook-free operations bell (F9-02): the badge, the popover with its
// Running / Pending / Completed sections, the collapse state, the cancel button
// and the activity dialog, over a feed of rows it is handed. Everything books
// owns arrives as a prop: the store (`useFeed`), what a status means
// (`status`), a row's name (`nameOf`), cancelling, the activity panel
// (`renderActivity`), the parsed message details (`renderDetails`) and the
// per-row actions (`rowActions`: Review, Undo). OperationsIndicator.tsx binds
// the books ones; this file names no audiobook store, service or route.

import { useState, type ReactNode } from 'react';
import { useNavigate } from 'react-router';
import {
  Alert,
  Badge,
  Box,
  Button,
  Chip,
  CircularProgress,
  Collapse,
  Dialog,
  DialogContent,
  DialogTitle,
  Divider,
  IconButton,
  LinearProgress,
  Popover,
  Tooltip,
  Typography,
} from '@mui/material';
import NotificationsIcon from '@mui/icons-material/Notifications';
import CancelIcon from '@mui/icons-material/Cancel';
import OpenInNewIcon from '@mui/icons-material/OpenInNew';
import HourglassEmptyIcon from '@mui/icons-material/HourglassEmpty';
import ArticleIcon from '@mui/icons-material/Article';
import CloseIcon from '@mui/icons-material/Close';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import ChevronRightIcon from '@mui/icons-material/ChevronRight';
import { storageKey } from '../app/appId';
import { byNewestFirst, type GroupableOperation } from '../providers';
import { formatProgressCounts } from './operationProgress';

/** A row the bell renders: the grouping row shape (ABO's ActiveOperation and the shell's OperationRow both fit). */
export type OperationsFrameRow = GroupableOperation;

/**
 * What the bell reads, as one hook's result. Every count comes from `raw` and
 * `alerts` (the ungrouped sets); only the rendered list comes from `grouped`.
 * `loadError` is set when the last refresh failed: the list is then the last
 * one the server confirmed, not the current state.
 */
export interface OperationsFeed {
  grouped: readonly OperationsFrameRow[];
  raw: readonly OperationsFrameRow[];
  alerts: readonly OperationsFrameRow[];
  loadError: string | null;
  reload: () => unknown;
}

/** What a status string means. The legacy operations API and core's task runs spell these differently. */
export interface RunStatusModel {
  /** Finished for good, or settled into an interrupted state: no longer in progress. */
  isTerminal: (status: string) => boolean;
  isQueued: (status: string) => boolean;
  /** Shown as the "success" chip. */
  isSuccess: (status: string) => boolean;
  /** Shown as the red chip. */
  isFailure: (status: string) => boolean;
}

/** What the activity dialog is showing. A group row carries its members, and
 *  the panel merges their transcripts; a real op carries none. */
export interface ActivityTarget {
  id: string;
  memberIds?: string[];
}

export interface OperationsIndicatorFrameProps {
  /** A hook with a stable identity (module-level): the frame calls it every render. */
  useFeed: () => OperationsFeed;
  status: RunStatusModel;
  /** A row's display name. */
  nameOf: (op: OperationsFrameRow) => string;
  /** Cancels a run by id. A rejection is swallowed: the next refresh shows the run as failed. */
  onCancel: (id: string) => Promise<unknown>;
  /** The body of the per-operation activity dialog. */
  renderActivity: (target: ActivityTarget) => ReactNode;
  /** Extra lines under a running row's progress counts (parsed message details). */
  renderDetails?: (op: OperationsFrameRow) => ReactNode;
  /** Buttons at the end of a completed row. `closePopover` closes the bell's popover. */
  rowActions?: (op: OperationsFrameRow, ctx: { closePopover: () => void }) => ReactNode;
  /** Where "View All" goes. */
  allOperationsPath: string;
  /** Where "Show all N in Activity" goes. */
  activityPath: string;
}

function formatETA(op: OperationsFrameRow): string | null {
  if (!op.startedAt || op.progress <= 0 || op.total <= 0) return null;
  const elapsed = (Date.now() - op.startedAt) / 1000;
  if (elapsed < 5) return null;
  const rate = op.progress / elapsed;
  if (rate <= 0) return null;
  const remaining = (op.total - op.progress) / rate;
  if (remaining < 60) return `~${Math.ceil(remaining)}s left`;
  if (remaining < 3600) return `~${Math.ceil(remaining / 60)}m left`;
  const h = Math.floor(remaining / 3600);
  const m = Math.ceil((remaining % 3600) / 60);
  return `~${h}h ${m}m left`;
}

function formatElapsed(op: OperationsFrameRow): string | null {
  if (!op.startedAt) return null;
  const sec = Math.floor((Date.now() - op.startedAt) / 1000);
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m ${sec % 60}s`;
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  return `${h}h ${m}m`;
}

/** The Completed section lists at most this many rows, newest first. A busy day
 *  leaves hundreds in the window and rendering them all made the popover a wall
 *  of rows; the rest are one click away on the Activity page. Running and
 *  pending rows are never capped. */
const COMPLETED_VISIBLE_CAP = 15;

type CollapseKey = 'running' | 'pending' | 'completed';

function activityTargetFor(op: OperationsFrameRow): ActivityTarget {
  return op.group ? { id: op.id, memberIds: op.group.memberIds } : { id: op.id };
}

// Under the app ID (07 §7.4.2), never the pre-shell literal key: two apps on one
// origin must not share it. Resolved per call because the app ID is set by
// createShell, after this module loads.
const collapseStorageKey = () => storageKey('operations', 'indicatorCollapse');

function loadCollapseState(): Record<CollapseKey, boolean> {
  // Defaults: Running open, Pending open, Completed collapsed.
  const defaults: Record<CollapseKey, boolean> = {
    running: true,
    pending: true,
    completed: false,
  };
  try {
    const raw = localStorage.getItem(collapseStorageKey());
    if (!raw) return defaults;
    const parsed = JSON.parse(raw);
    return { ...defaults, ...parsed };
  } catch {
    return defaults;
  }
}

function persistCollapseState(state: Record<CollapseKey, boolean>) {
  try {
    localStorage.setItem(collapseStorageKey(), JSON.stringify(state));
  } catch {
    /* ignore quota errors */
  }
}

function SectionHeader({
  label,
  count,
  open,
  onToggle,
}: {
  label: string;
  count: number;
  open: boolean;
  onToggle: () => void;
}) {
  return (
    <Box
      onClick={onToggle}
      sx={{
        px: 2,
        pt: 1,
        pb: 0.5,
        display: 'flex',
        alignItems: 'center',
        gap: 0.5,
        cursor: 'pointer',
        userSelect: 'none',
        '&:hover': { bgcolor: 'action.hover' },
      }}
    >
      {open ? (
        <ExpandMoreIcon sx={{ fontSize: 16, color: 'text.secondary' }} />
      ) : (
        <ChevronRightIcon sx={{ fontSize: 16, color: 'text.secondary' }} />
      )}
      <Typography
        variant="caption"
        sx={{
          color: 'text.secondary',
          textTransform: 'uppercase',
          fontSize: '0.65rem',
          letterSpacing: '0.08em',
          fontWeight: 600,
        }}
      >
        {label} ({count})
      </Typography>
    </Box>
  );
}

export function OperationsIndicatorFrame({
  useFeed,
  status,
  nameOf,
  onCancel,
  renderActivity,
  renderDetails,
  rowActions,
  allOperationsPath,
  activityPath,
}: OperationsIndicatorFrameProps) {
  // groupedOperations is the rendered list. rawOperations is the ungrouped set:
  // every COUNT in this popover comes from it, and the badge from
  // alertOperations.
  const {
    grouped: groupedOperations,
    raw: rawOperations,
    alerts: alertOperations,
    // Set when the last timeline refresh failed. The list is then the last one
    // the server confirmed, not the current state — the popover says so rather
    // than showing "No operations" over a server that is down (WEB-05).
    loadError,
    reload: loadFromServer,
  } = useFeed();
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null);
  const [cancelling, setCancelling] = useState<Set<string>>(new Set());
  const [activityTarget, setActivityTarget] = useState<ActivityTarget | null>(null);
  const [collapse, setCollapse] = useState<Record<CollapseKey, boolean>>(loadCollapseState);
  const navigate = useNavigate();

  const toggleSection = (key: CollapseKey) => {
    setCollapse((prev) => {
      const next = { ...prev, [key]: !prev[key] };
      persistCollapseState(next);
      return next;
    });
  };

  const handleCancel = async (opId: string) => {
    setCancelling((prev) => new Set(prev).add(opId));
    try {
      await onCancel(opId);
    } catch {
      // Will show as failed in next poll
    }
    setCancelling((prev) => {
      const next = new Set(prev);
      next.delete(opId);
      return next;
    });
  };

  // status.isTerminal, not the three-status literal these used to enumerate. The
  // backend mints a family of interrupted_* statuses (one per ResumePolicy),
  // and none of them was in that list — so an op that had FINISHED as
  // interrupted_dropped was counted as in-progress here forever: a badge stuck
  // one too high, an entry sitting in "running" with a dead progress bar, and
  // nothing in the terminal section. operationPolling.ts's own comment warns
  // against enumerating for exactly this reason; these three sites predate it.
  // The activity migration ends at interrupted_dropped on every restart, which
  // is what surfaced it.
  const alertInProgress = alertOperations.filter((op) => !status.isTerminal(op.status));
  // The badge counts REAL work, from the ungrouped alert set. Deriving it from
  // grouped rows would make it disagree with the list it labels: twelve queued
  // runs are twelve things happening, however many rows it takes to show them.
  const badgeCount = alertInProgress.length;

  // The LIST is grouped, and shows top-level rows only. This popover has no
  // tree rendering — no indentation, no expander — so a group's children would
  // land in it as duplicate flat rows. The parent alone is the roll-up, and the
  // Activity page is where the members are.
  // NEWEST FIRST, and it has to happen here. groupOperations emits bucket by
  // bucket (every row of one def_id+status, then the next kind), so the list it
  // returns is ordered by KIND and carries no time order at all — which is why
  // the newest completion used to land wherever its kind's bucket happened to
  // fall, usually at the bottom of the popover where nobody saw it. The Activity
  // page already sorts its own history sections by finishedAt; this is the bell
  // catching up to it, through the same groupTimestamp both folds use.
  const rows = groupedOperations.filter((op) => !op.parent_id).sort(byNewestFirst);
  const inProgress = rows.filter((op) => !status.isTerminal(op.status));
  const queued = inProgress.filter((op) => status.isQueued(op.status));
  const running = inProgress.filter((op) => !status.isQueued(op.status));
  const terminal = rows.filter((op) => status.isTerminal(op.status));
  const terminalShown = terminal.slice(0, COMPLETED_VISIBLE_CAP);
  const terminalCapped = terminal.length > COMPLETED_VISIBLE_CAP;

  // The section HEADINGS count operations, from the ungrouped set, for the same
  // reason the badge does: a group row stands for many runs, so counting rows
  // would put "Completed (1)" over a row whose own chip says "×12". Each count
  // uses the same predicate as the list beneath it, applied to the raw set.
  const rawInProgress = rawOperations.filter((op) => !status.isTerminal(op.status));
  const queuedCount = rawInProgress.filter((op) => status.isQueued(op.status)).length;
  const runningCount = rawInProgress.length - queuedCount;
  const terminalCount = rawOperations.length - rawInProgress.length;

  const empty = running.length === 0 && queued.length === 0 && terminal.length === 0;

  const tooltip =
    badgeCount > 0
      ? `${badgeCount} active operation${badgeCount !== 1 ? 's' : ''}`
      : 'No active operations';

  return (
    <>
      <Tooltip title={loadError ? `${tooltip} — last refresh failed` : tooltip}>
        <IconButton
          color="inherit"
          onClick={(e) => setAnchorEl(e.currentTarget)}
          sx={{ mr: 1 }}
          data-testid="operations-bell"
        >
          {/* The count badge is what it always was. The error DOT is separate
              so a failed refresh is visible on the closed bell without
              pretending to be an operation count. */}
          <Badge
            variant="dot"
            color="error"
            invisible={!loadError}
            overlap="circular"
            anchorOrigin={{ vertical: 'bottom', horizontal: 'right' }}
            data-testid="operations-bell-error-dot"
          >
            <Badge badgeContent={badgeCount > 0 ? badgeCount : undefined} color="warning">
              {badgeCount > 0 ? (
                <CircularProgress size={24} color="inherit" thickness={4} />
              ) : (
                <NotificationsIcon />
              )}
            </Badge>
          </Badge>
        </IconButton>
      </Tooltip>
      <Popover
        open={Boolean(anchorEl)}
        anchorEl={anchorEl}
        onClose={() => setAnchorEl(null)}
        anchorOrigin={{ vertical: 'bottom', horizontal: 'right' }}
        transformOrigin={{ vertical: 'top', horizontal: 'right' }}
      >
        <Box sx={{ minWidth: 400, maxWidth: 480 }}>
          {/* Header */}
          <Box
            sx={{
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'center',
              px: 2,
              pt: 1.5,
              pb: 1,
            }}
          >
            <Typography variant="subtitle2">Operations</Typography>
            <Button
              size="small"
              endIcon={<OpenInNewIcon sx={{ fontSize: 14 }} />}
              onClick={() => {
                setAnchorEl(null);
                navigate(allOperationsPath);
              }}
              sx={{ textTransform: 'none', fontSize: '0.75rem' }}
            >
              View All
            </Button>
          </Box>

          <Divider />

          {/* Three DISTINGUISHABLE states for a list that used to have one:
                error + nothing to show — the request failed and we have no
                                          confirmed list; say so, offer Retry
                error + a list          — stale: show the warning ABOVE the
                                          last confirmed list, do not blank it
                no error + empty        — the server really has nothing
              Until 2026-09-11 the fetch returned [] on failure, so the first
              case rendered as the third. */}
          {loadError && (
            <Alert
              severity={empty ? 'error' : 'warning'}
              data-testid="operations-load-error"
              sx={{ m: 1 }}
              action={
                <Button color="inherit" size="small" onClick={() => void loadFromServer()}>
                  Retry
                </Button>
              }
            >
              {empty
                ? `Could not load operations: ${loadError}`
                : `Could not refresh operations — this list may be out of date: ${loadError}`}
            </Alert>
          )}

          {empty && !loadError && (
            <Typography
              variant="body2"
              data-testid="operations-empty"
              sx={{
                color: 'text.secondary',
                px: 2,
                py: 3,
                textAlign: 'center',
              }}
            >
              No operations
            </Typography>
          )}

          {/* ===== RUNNING ===== */}
          {running.length > 0 && (
            <>
              <SectionHeader
                label="Running"
                count={runningCount}
                open={collapse.running}
                onToggle={() => toggleSection('running')}
              />
              <Collapse in={collapse.running} unmountOnExit>
                {running.map((op: OperationsFrameRow) => {
                  const progressPct = op.total > 0 ? Math.round((op.progress / op.total) * 100) : 0;
                  const eta = formatETA(op);
                  const elapsed = formatElapsed(op);

                  return (
                    <Box
                      key={op.id}
                      sx={{
                        px: 2,
                        py: 1.5,
                        '&:not(:last-child)': {
                          borderBottom: '1px solid',
                          borderColor: 'divider',
                        },
                      }}
                    >
                      <Box
                        sx={{
                          display: 'flex',
                          justifyContent: 'space-between',
                          alignItems: 'center',
                          mb: 0.5,
                        }}
                      >
                        <Typography
                          variant="body2"
                          sx={{
                            fontWeight: 'bold',
                          }}
                        >
                          {nameOf(op)}
                          {op.group ? ` ×${op.group.count}` : ''}
                        </Typography>
                        <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
                          {elapsed && (
                            <Typography
                              variant="caption"
                              sx={{
                                color: 'text.secondary',
                              }}
                            >
                              {elapsed}
                            </Typography>
                          )}
                          {/* A group row opens the MERGED log of its members
                              (the panel takes their ids), so the view works for
                              both. Cancel still acts on op.id against the
                              server, and a group's id names no record. */}
                          <Tooltip title={op.group ? 'View merged activity' : 'View activity'}>
                            <IconButton
                              size="small"
                              onClick={(e) => {
                                e.stopPropagation();
                                setActivityTarget(activityTargetFor(op));
                              }}
                              sx={{ p: 0.25 }}
                            >
                              <ArticleIcon sx={{ fontSize: 18 }} />
                            </IconButton>
                          </Tooltip>
                          {!op.group && (
                            <Tooltip title="Cancel">
                              <IconButton
                                size="small"
                                color="error"
                                onClick={() => handleCancel(op.id)}
                                disabled={cancelling.has(op.id)}
                                sx={{ p: 0.25 }}
                              >
                                {cancelling.has(op.id) ? (
                                  <CircularProgress size={14} />
                                ) : (
                                  <CancelIcon sx={{ fontSize: 18 }} />
                                )}
                              </IconButton>
                            </Tooltip>
                          )}
                        </Box>
                      </Box>

                      {op.total > 0 ? (
                        <LinearProgress
                          variant="determinate"
                          value={progressPct}
                          sx={{ height: 6, borderRadius: 1, mb: 0.5 }}
                        />
                      ) : (
                        <LinearProgress sx={{ height: 6, borderRadius: 1, mb: 0.5 }} />
                      )}

                      <Box
                        sx={{
                          display: 'flex',
                          justifyContent: 'space-between',
                          alignItems: 'center',
                          mb: 0.25,
                        }}
                      >
                        <Typography
                          variant="caption"
                          sx={{
                            color: 'text.secondary',
                            fontFamily: 'monospace',
                          }}
                        >
                          {formatProgressCounts(op)}
                        </Typography>
                        {eta && (
                          <Typography
                            variant="caption"
                            sx={{
                              color: 'text.secondary',
                              fontStyle: 'italic',
                            }}
                          >
                            {eta}
                          </Typography>
                        )}
                      </Box>

                      {renderDetails?.(op)}
                    </Box>
                  );
                })}
              </Collapse>
            </>
          )}

          {/* ===== PENDING ===== */}
          {queued.length > 0 && (
            <>
              <SectionHeader
                label="Pending"
                count={queuedCount}
                open={collapse.pending}
                onToggle={() => toggleSection('pending')}
              />
              <Collapse in={collapse.pending} unmountOnExit>
                {queued.map((op: OperationsFrameRow) => (
                  <Box
                    key={op.id}
                    sx={{
                      px: 2,
                      py: 1,
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      '&:not(:last-child)': {
                        borderBottom: '1px solid',
                        borderColor: 'divider',
                      },
                    }}
                  >
                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                      <HourglassEmptyIcon sx={{ fontSize: 16, color: 'text.secondary' }} />
                      <Box>
                        <Typography
                          variant="body2"
                          sx={{
                            fontWeight: 'bold',
                          }}
                        >
                          {nameOf(op)}
                          {op.group ? ` ×${op.group.count}` : ''}
                        </Typography>
                        {/* A queued op reports how much work it is holding via
                            progress_message (OperationDef.SummarizeQueued), and
                            for a batch apply that number GROWS while it waits —
                            the queue merger keeps unioning newly approved books
                            into this very row. Show it when the server sent one;
                            "Waiting to start…" is only the fallback for ops that
                            cannot say how big they are. */}
                        <Typography
                          variant="caption"
                          sx={{
                            color: 'text.secondary',
                          }}
                        >
                          {op.message || 'Waiting to start…'}
                        </Typography>
                      </Box>
                    </Box>
                    {/* A group row's id names no server record — see the
                        queued/running rows above. */}
                    {!op.group && (
                      <Tooltip title="Cancel">
                        <IconButton
                          size="small"
                          color="error"
                          onClick={() => handleCancel(op.id)}
                          disabled={cancelling.has(op.id)}
                          sx={{ p: 0.25 }}
                        >
                          {cancelling.has(op.id) ? (
                            <CircularProgress size={14} />
                          ) : (
                            <CancelIcon sx={{ fontSize: 18 }} />
                          )}
                        </IconButton>
                      </Tooltip>
                    )}
                  </Box>
                ))}
              </Collapse>
            </>
          )}

          {/* ===== COMPLETED ===== */}
          {terminal.length > 0 && (
            <>
              <SectionHeader
                label="Completed"
                count={terminalCount}
                open={collapse.completed}
                onToggle={() => toggleSection('completed')}
              />
              <Collapse in={collapse.completed} unmountOnExit>
                {terminalShown.map((op: OperationsFrameRow) => {
                  const statusLabel = status.isSuccess(op.status) ? 'success' : op.status;
                  const statusColor =
                    status.isSuccess(op.status)
                      ? ('success' as const)
                      : status.isFailure(op.status)
                        ? ('error' as const)
                        : ('default' as const);
                  return (
                    <Box
                      key={`recent-${op.id}`}
                      // A group row opens the merged log of its members; a
                      // real op opens its own. Either way the panel gets what
                      // it needs to fetch, so the synthetic id is never sent
                      // to the server.
                      onClick={() => setActivityTarget(activityTargetFor(op))}
                      sx={{
                        px: 2,
                        py: 0.75,
                        cursor: 'pointer',
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'space-between',
                        gap: 1,
                        '&:hover': { bgcolor: 'action.hover' },
                        '&:not(:last-child)': {
                          borderBottom: '1px solid',
                          borderColor: 'divider',
                        },
                      }}
                    >
                      <Typography
                        variant="caption"
                        noWrap
                        sx={{
                          fontWeight: 'bold',
                          flex: 1,
                        }}
                      >
                        {nameOf(op)}
                        {op.group ? ` ×${op.group.count}` : ''}
                      </Typography>
                      <Chip
                        label={statusLabel}
                        size="small"
                        color={statusColor}
                        sx={{
                          height: 18,
                          fontSize: '0.65rem',
                          '& .MuiChip-label': { px: 0.75 },
                        }}
                      />
                      {rowActions?.(op, { closePopover: () => setAnchorEl(null) })}
                    </Box>
                  );
                })}
                {terminalCapped && (
                  <Box sx={{ px: 2, py: 0.75 }}>
                    <Button
                      size="small"
                      data-testid="operations-show-all-completed"
                      onClick={() => {
                        setAnchorEl(null);
                        navigate(activityPath);
                      }}
                      sx={{ textTransform: 'none', fontSize: '0.75rem' }}
                    >
                      Show all {terminalCount} in Activity
                    </Button>
                  </Box>
                )}
              </Collapse>
            </>
          )}
        </Box>
      </Popover>

      {/* Per-operation activity dialog — surfaces the focused
          /api/v1/operations/:id/activity feed (or, for a group row, the
          merged feed of its members) without navigating away. */}
      <Dialog
        open={activityTarget !== null}
        onClose={() => setActivityTarget(null)}
        maxWidth="md"
        fullWidth
      >
        <DialogTitle sx={{ pr: 6 }}>
          {activityTarget?.memberIds
            ? `Merged Activity (${activityTarget.memberIds.length} runs)`
            : 'Operation Activity'}
          <IconButton
            aria-label="Close"
            onClick={() => setActivityTarget(null)}
            sx={{ position: 'absolute', right: 8, top: 8 }}
          >
            <CloseIcon />
          </IconButton>
        </DialogTitle>
        <DialogContent dividers>
          {activityTarget && renderActivity(activityTarget)}
        </DialogContent>
      </Dialog>
    </>
  );
}
