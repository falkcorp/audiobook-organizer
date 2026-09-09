// file: web/src/components/layout/OperationsIndicator.tsx
// version: 4.9.0
// guid: 3b4c5d6e-7f8a-9b0c-1d2e-3f4a5b6c7d8e
// last-edited: 2026-09-09

import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
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
import { OperationActivityPanel } from '../OperationActivityPanel';
import { useOperationsStore, type ActiveOperation } from '../../stores/useOperationsStore';
import { formatProgressCounts, operationDisplayName } from './operationsFormat';
import { isTerminal } from '../../utils/operationPolling';
import { cancelOperation } from '../../services/api';
import { getUndoPreflight, revertOperation as revertOp } from '../../services/versionApi';

function formatETA(op: ActiveOperation): string | null {
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

function formatElapsed(op: ActiveOperation): string | null {
  if (!op.startedAt) return null;
  const sec = Math.floor((Date.now() - op.startedAt) / 1000);
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m ${sec % 60}s`;
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  return `${h}h ${m}m`;
}

function parseMessageDetails(message: string) {
  const titleMatch = message.match(/—\s*(.+)$/);
  const countsMatch = message.match(/\(imported (\d+), skipped (\d+), failed (\d+)\)/);
  // OL import: "Importing editions: 1234k records"
  const olMatch = message.match(/Importing (\w+): (\d+)k records/);
  return {
    currentTitle: titleMatch ? titleMatch[1] : null,
    imported: countsMatch ? parseInt(countsMatch[1]) : null,
    skipped: countsMatch ? parseInt(countsMatch[2]) : null,
    failed: countsMatch ? parseInt(countsMatch[3]) : null,
    olType: olMatch ? olMatch[1] : null,
    olRecords: olMatch ? `${olMatch[2]}k` : null,
  };
}

type CollapseKey = 'running' | 'pending' | 'completed';

const COLLAPSE_STORAGE_KEY = 'ops-indicator-collapse-v1';

function loadCollapseState(): Record<CollapseKey, boolean> {
  // Defaults: Running open, Pending open, Completed collapsed.
  const defaults: Record<CollapseKey, boolean> = {
    running: true,
    pending: true,
    completed: false,
  };
  try {
    const raw = localStorage.getItem(COLLAPSE_STORAGE_KEY);
    if (!raw) return defaults;
    const parsed = JSON.parse(raw);
    return { ...defaults, ...parsed };
  } catch {
    return defaults;
  }
}

function persistCollapseState(state: Record<CollapseKey, boolean>) {
  try {
    localStorage.setItem(COLLAPSE_STORAGE_KEY, JSON.stringify(state));
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

export function OperationsIndicator() {
  const groupedOperations = useOperationsStore((state) => state.groupedOperations);
  // The ungrouped set. Every COUNT in this popover comes from here; only the
  // rendered list comes from groupedOperations.
  const rawOperations = useOperationsStore((state) => state.activeOperations);
  const alertOperations = useOperationsStore((state) => state.alertOperations);
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null);
  const [cancelling, setCancelling] = useState<Set<string>>(new Set());
  const [activityOpId, setActivityOpId] = useState<string | null>(null);
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
      await cancelOperation(opId);
    } catch {
      // Will show as failed in next poll
    }
    setCancelling((prev) => {
      const next = new Set(prev);
      next.delete(opId);
      return next;
    });
  };

  // isTerminal, not the three-status literal these used to enumerate. The
  // backend mints a family of interrupted_* statuses (one per ResumePolicy),
  // and none of them was in that list — so an op that had FINISHED as
  // interrupted_dropped was counted as in-progress here forever: a badge stuck
  // one too high, an entry sitting in "running" with a dead progress bar, and
  // nothing in the terminal section. operationPolling.ts's own comment warns
  // against enumerating for exactly this reason; these three sites predate it.
  // The activity migration ends at interrupted_dropped on every restart, which
  // is what surfaced it.
  const alertInProgress = alertOperations.filter((op) => !isTerminal(op.status));
  // The badge counts REAL work, from the ungrouped alert set. Deriving it from
  // grouped rows would make it disagree with the list it labels: twelve queued
  // runs are twelve things happening, however many rows it takes to show them.
  const badgeCount = alertInProgress.length;

  // The LIST is grouped, and shows top-level rows only. This popover has no
  // tree rendering — no indentation, no expander — so a group's children would
  // land in it as duplicate flat rows. The parent alone is the roll-up, and the
  // Activity page is where the members are.
  const rows = groupedOperations.filter((op) => !op.parent_id);
  const inProgress = rows.filter((op) => !isTerminal(op.status));
  const queued = inProgress.filter((op) => op.status === 'queued');
  const running = inProgress.filter((op) => op.status !== 'queued');
  const terminal = rows.filter((op) => isTerminal(op.status));

  // The section HEADINGS count operations, from the ungrouped set, for the same
  // reason the badge does: a group row stands for many runs, so counting rows
  // would put "Completed (1)" over a row whose own chip says "×12". Each count
  // uses the same predicate as the list beneath it, applied to the raw set.
  const rawInProgress = rawOperations.filter((op) => !isTerminal(op.status));
  const queuedCount = rawInProgress.filter((op) => op.status === 'queued').length;
  const runningCount = rawInProgress.length - queuedCount;
  const terminalCount = rawOperations.length - rawInProgress.length;

  const empty = running.length === 0 && queued.length === 0 && terminal.length === 0;

  return (
    <>
      <Tooltip
        title={
          badgeCount > 0
            ? `${badgeCount} active operation${badgeCount !== 1 ? 's' : ''}`
            : 'No active operations'
        }
      >
        <IconButton color="inherit" onClick={(e) => setAnchorEl(e.currentTarget)} sx={{ mr: 1 }}>
          <Badge badgeContent={badgeCount > 0 ? badgeCount : undefined} color="warning">
            {badgeCount > 0 ? (
              <CircularProgress size={24} color="inherit" thickness={4} />
            ) : (
              <NotificationsIcon />
            )}
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
                navigate('/operations');
              }}
              sx={{ textTransform: 'none', fontSize: '0.75rem' }}
            >
              View All
            </Button>
          </Box>

          <Divider />

          {empty && (
            <Typography
              variant="body2"
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
                {running.map((op: ActiveOperation) => {
                  const progressPct = op.total > 0 ? Math.round((op.progress / op.total) * 100) : 0;
                  const eta = formatETA(op);
                  const elapsed = formatElapsed(op);
                  const details = parseMessageDetails(op.message);

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
                          {operationDisplayName(op)}
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
                          {/* Both act on op.id against the server, and a group
                              row's id is derived from its members — it names no
                              record. The group's runs are on the Activity page,
                              which "View All" already goes to. */}
                          {!op.group && (
                            <Tooltip title="View activity">
                              <IconButton
                                size="small"
                                onClick={(e) => {
                                  e.stopPropagation();
                                  setActivityOpId(op.id);
                                }}
                                sx={{ p: 0.25 }}
                              >
                                <ArticleIcon sx={{ fontSize: 18 }} />
                              </IconButton>
                            </Tooltip>
                          )}
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

                      {details.imported !== null && (
                        <Typography variant="caption" sx={{ display: 'block', mb: 0.25 }}>
                          <Box component="span" sx={{ color: 'success.main' }}>
                            {details.imported} imported
                          </Box>
                          {details.skipped! > 0 && (
                            <Box component="span" sx={{ color: 'text.secondary', ml: 1 }}>
                              {details.skipped} skipped
                            </Box>
                          )}
                          {details.failed! > 0 && (
                            <Box component="span" sx={{ color: 'error.main', ml: 1 }}>
                              {details.failed} failed
                            </Box>
                          )}
                        </Typography>
                      )}

                      {details.olType && (
                        <Typography
                          variant="caption"
                          sx={{
                            color: 'info.main',
                            display: 'block',
                            mb: 0.25,
                          }}
                        >
                          {details.olType}: {details.olRecords} records
                        </Typography>
                      )}

                      {details.currentTitle && (
                        <Typography
                          variant="caption"
                          noWrap
                          title={details.currentTitle}
                          sx={{
                            color: 'primary.main',
                            display: 'block',
                          }}
                        >
                          {details.currentTitle}
                        </Typography>
                      )}
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
                {queued.map((op: ActiveOperation) => (
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
                          {operationDisplayName(op)}
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
                {terminal.map((op: ActiveOperation) => {
                  const statusLabel = op.status === 'completed' ? 'success' : op.status;
                  const statusColor =
                    op.status === 'completed'
                      ? ('success' as const)
                      : op.status === 'failed'
                        ? ('error' as const)
                        : ('default' as const);
                  return (
                    <Box
                      key={`recent-${op.id}`}
                      // A group row opens nothing: its id names no record, so
                      // the activity panel would query an operation that does
                      // not exist. The Activity page holds its members.
                      onClick={op.group ? undefined : () => setActivityOpId(op.id)}
                      sx={{
                        px: 2,
                        py: 0.75,
                        cursor: op.group ? 'default' : 'pointer',
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
                        {operationDisplayName(op)}
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
                      {op.type === 'metadata_candidate_fetch' && op.status === 'completed' && (
                        <Button
                          size="small"
                          variant="outlined"
                          sx={{
                            textTransform: 'none',
                            fontSize: '0.65rem',
                            py: 0,
                            minHeight: 20,
                          }}
                          onClick={(e) => {
                            e.stopPropagation();
                            e.preventDefault();
                            setAnchorEl(null);
                            // Was `window.location.href = '/library?reviewOp=' + op.id`,
                            // which reloaded the whole SPA to open a modal over
                            // the library. The op id was never used for anything
                            // but its own presence -- Library read it as a
                            // boolean -- so nothing is lost by dropping it.
                            navigate('/review');
                          }}
                        >
                          Review
                        </Button>
                      )}
                      {(op.type === 'organize' || op.type === 'scan_and_organize') &&
                        op.status === 'completed' && (
                          <Button
                            size="small"
                            variant="outlined"
                            color="warning"
                            sx={{
                              textTransform: 'none',
                              fontSize: '0.65rem',
                              py: 0,
                              minHeight: 20,
                            }}
                            onClick={async (e) => {
                              e.stopPropagation();
                              e.preventDefault();
                              try {
                                const preflight = await getUndoPreflight(op.id);
                                const conflicts =
                                  (preflight.content_changed?.length || 0) +
                                  (preflight.book_deleted?.length || 0) +
                                  (preflight.re_organized?.length || 0);
                                const msg =
                                  conflicts > 0
                                    ? `${preflight.safe} changes can be undone. ${conflicts} conflict(s) detected. Proceed?`
                                    : `Undo ${preflight.safe} change(s) from this operation?`;
                                if (confirm(msg)) {
                                  await revertOp(op.id);
                                  alert('Operation reverted successfully');
                                }
                              } catch (err: unknown) {
                                const msg = (err as { message?: string })?.message || 'Undo failed';
                                alert(msg);
                              }
                            }}
                          >
                            Undo
                          </Button>
                        )}
                    </Box>
                  );
                })}
              </Collapse>
            </>
          )}
        </Box>
      </Popover>

      {/* Per-operation activity dialog — surfaces the focused
          /api/v1/operations/:id/activity feed without navigating away. */}
      <Dialog
        open={activityOpId !== null}
        onClose={() => setActivityOpId(null)}
        maxWidth="md"
        fullWidth
      >
        <DialogTitle sx={{ pr: 6 }}>
          Operation Activity
          <IconButton
            aria-label="Close"
            onClick={() => setActivityOpId(null)}
            sx={{ position: 'absolute', right: 8, top: 8 }}
          >
            <CloseIcon />
          </IconButton>
        </DialogTitle>
        <DialogContent dividers>
          {activityOpId && <OperationActivityPanel operationId={activityOpId} />}
        </DialogContent>
      </Dialog>
    </>
  );
}
