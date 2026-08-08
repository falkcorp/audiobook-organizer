// file: web/src/components/layout/OperationsIndicator.tsx
// version: 4.0.1
// guid: 3b4c5d6e-7f8a-9b0c-1d2e-3f4a5b6c7d8e

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
import {
  useOperationsStore,
  type ActiveOperation,
} from '../../stores/useOperationsStore';
import {
  cancelOperation,
} from '../../services/api';
import { getUndoPreflight, revertOperation as revertOp } from '../../services/versionApi';

function formatOperationType(type: string): string {
  switch (type) {
    case 'itunes_import':
      return 'iTunes Import';
    case 'itunes_sync':
      return 'iTunes Sync';
    case 'scan':
      return 'Library Scan';
    case 'organize':
      return 'Organize';
    case 'metadata_fetch':
      return 'Metadata Fetch';
    case 'metadata_candidate_fetch':
      return 'Metadata Fetch (Batch)';
    case 'ol_dump_import':
      return 'Open Library Import';
    case 'dedup-scan':
      return 'Dedup Scan';
    case 'dedup-llm-review':
      return 'Dedup AI Review';
    case 'dedup-acoustid-scan':
      return 'AcoustID Scan';
    case 'dedup-book-signature-scan':
      return 'Book Signature Scan';
    case 'embed-scan':
      return 'Embedding Rescan';
    case 'fingerprint-rescan':
      return 'Fingerprint Rescan';
    default:
      return type.replace(/_/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase());
  }
}

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
  const countsMatch = message.match(
    /\(imported (\d+), skipped (\d+), failed (\d+)\)/
  );
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
        color="text.secondary"
        sx={{
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
  const activeOperations = useOperationsStore((state) => state.activeOperations);
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

  const alertInProgress = alertOperations.filter(
    (op) => !['completed', 'failed', 'canceled'].includes(op.status)
  );
  const badgeCount = alertInProgress.length;

  const inProgress = activeOperations.filter(
    (op) => !['completed', 'failed', 'canceled'].includes(op.status)
  );
  const queued = inProgress.filter((op) => op.status === 'queued');
  const running = inProgress.filter((op) => op.status !== 'queued');
  const terminal = activeOperations.filter((op) =>
    ['completed', 'failed', 'canceled'].includes(op.status)
  );

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
        <IconButton
          color="inherit"
          onClick={(e) => setAnchorEl(e.currentTarget)}
          sx={{ mr: 1 }}
        >
          <Badge
            badgeContent={badgeCount > 0 ? badgeCount : undefined}
            color="warning"
          >
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
              color="text.secondary"
              sx={{ px: 2, py: 3, textAlign: 'center' }}
            >
              No operations
            </Typography>
          )}

          {/* ===== RUNNING ===== */}
          {running.length > 0 && (
            <>
              <SectionHeader
                label="Running"
                count={running.length}
                open={collapse.running}
                onToggle={() => toggleSection('running')}
              />
              <Collapse in={collapse.running} unmountOnExit>
                {running.map((op: ActiveOperation) => {
                  const progressPct =
                    op.total > 0 ? Math.round((op.progress / op.total) * 100) : 0;
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
                        <Typography variant="body2" fontWeight="bold">
                          {formatOperationType(op.type)}
                        </Typography>
                        <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
                          {elapsed && (
                            <Typography variant="caption" color="text.secondary">
                              {elapsed}
                            </Typography>
                          )}
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
                          color="text.secondary"
                          sx={{ fontFamily: 'monospace' }}
                        >
                          {op.total > 0 ? (
                            <>
                              {op.progress.toLocaleString()} / {op.total.toLocaleString()}
                              {' '}({progressPct}%)
                            </>
                          ) : (
                            'Starting...'
                          )}
                        </Typography>
                        {eta && (
                          <Typography
                            variant="caption"
                            color="text.secondary"
                            fontStyle="italic"
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
                          color="info.main"
                          sx={{ display: 'block', mb: 0.25 }}
                        >
                          {details.olType}: {details.olRecords} records
                        </Typography>
                      )}

                      {details.currentTitle && (
                        <Typography
                          variant="caption"
                          color="primary.main"
                          display="block"
                          noWrap
                          title={details.currentTitle}
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
                count={queued.length}
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
                        <Typography variant="body2" fontWeight="bold">
                          {formatOperationType(op.type)}
                        </Typography>
                        <Typography variant="caption" color="text.secondary">
                          Waiting to start…
                        </Typography>
                      </Box>
                    </Box>
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
                count={terminal.length}
                open={collapse.completed}
                onToggle={() => toggleSection('completed')}
              />
              <Collapse in={collapse.completed} unmountOnExit>
                {terminal.map((op: ActiveOperation) => {
                  const statusLabel =
                    op.status === 'completed' ? 'success' : op.status;
                  const statusColor =
                    op.status === 'completed'
                      ? ('success' as const)
                      : op.status === 'failed'
                        ? ('error' as const)
                        : ('default' as const);
                  return (
                    <Box
                      key={`recent-${op.id}`}
                      onClick={() => setActivityOpId(op.id)}
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
                        fontWeight="bold"
                        noWrap
                        sx={{ flex: 1 }}
                      >
                        {formatOperationType(op.type)}
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
                      {op.type === 'metadata_candidate_fetch' &&
                        op.status === 'completed' && (
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
                              window.location.href = `/library?reviewOp=${op.id}`;
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
                                const msg =
                                  (err as { message?: string })?.message || 'Undo failed';
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
