// file: web/src/components/OperationActivityPanel.tsx
// version: 1.5.0
// guid: f7a1e2c3-9b4d-4e5a-8c6f-1d3b5a7e9c0f
// last-edited: 2026-09-10

import { useCallback, useEffect, useState, useRef, useMemo } from 'react';
import {
  Box,
  Button,
  Chip,
  CircularProgress,
  Collapse,
  IconButton,
  Paper,
  Stack,
  Tooltip,
  Typography,
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import ChevronRightIcon from '@mui/icons-material/ChevronRight';
import RefreshIcon from '@mui/icons-material/Refresh';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import {
  fetchMergedOperationActivity,
  fetchOperationActivity,
  type OperationActivityEntry,
} from '../services/activityApi';
import { useOperationsStore } from '../stores/useOperationsStore';
import { useToast } from './toast/ToastProvider';

interface OperationActivityPanelProps {
  /** Operation ID to fetch the per-op activity feed for. For a group row this
   *  is the synthetic group id (see operationGrouping.ts): it names no server
   *  record and is used only to find the row in the store and to key the view. */
  operationId: string;
  /** Present for a GROUP row: the real operations behind it, oldest first. The
   *  panel then shows one merged timeline of all of them instead of a single
   *  op's feed, and every entry is labelled with the member it came from. */
  memberIds?: string[];
  /** Optional cap on entries returned by the server (default 100 for one op,
   *  MERGED_DEFAULT_LIMIT for a group). */
  limit?: number;
}

/** A group's members usually log a few lines each, so a cap sized for one op
 *  would show only the last member's tail. */
const MERGED_DEFAULT_LIMIT = 2000;

/** shortId is the member label on a merged entry: enough of a ULID to tell
 *  neighbours apart, short enough to sit on every line. */
function shortId(id: string): string {
  return id.slice(0, 8);
}

function levelChip(level: string) {
  const colorMap: Record<string, 'error' | 'warning' | 'info' | 'default'> = {
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
      sx={{ minWidth: 60 }}
    />
  );
}

function rowBgColor(level: string): string | undefined {
  if (level === 'error') return 'rgba(211, 47, 47, 0.08)';
  if (level === 'warn' || level === 'warning') return 'rgba(237, 108, 2, 0.08)';
  return undefined;
}

function formatTimestamp(ts: string): string {
  const d = new Date(ts);
  if (isNaN(d.getTime())) return ts;
  return d.toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
}

function statusColor(status: string): 'success' | 'error' | 'warning' | 'info' | 'default' {
  if (status === 'completed') return 'success';
  if (status === 'failed') return 'error';
  if (status === 'canceled') return 'warning';
  if (status === 'queued') return 'default';
  return 'info';
}

function inferStatusFromEntries(entries: OperationActivityEntry[]): string {
  if (entries.length === 0) return 'unknown';
  const last = entries[entries.length - 1];
  if (last.level === 'error') return 'failed';
  return 'completed';
}

interface EntryRowProps {
  entry: OperationActivityEntry;
  /** Set when the panel shows a merged timeline: which member wrote the line. */
  memberLabel?: string;
}

function EntryRow({ entry, memberLabel }: EntryRowProps) {
  const [expanded, setExpanded] = useState(false);
  const hasDetails = Boolean(entry.details && entry.details.trim().length > 0);

  return (
    <Box
      sx={{
        bgcolor: rowBgColor(entry.level),
        borderBottom: '1px solid',
        borderColor: 'divider',
        px: 1.5,
        py: 0.75,
      }}
    >
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
            fontFamily: 'monospace',
            color: 'text.secondary',
            minWidth: 80,
            flexShrink: 0,
          }}
        >
          {formatTimestamp(entry.timestamp)}
        </Typography>
        {memberLabel !== undefined && (
          <Tooltip title={`Operation ${entry.operation_id}`}>
            <Typography
              variant="caption"
              sx={{
                fontFamily: 'monospace',
                color: 'text.secondary',
                minWidth: 64,
                flexShrink: 0,
              }}
            >
              {memberLabel}
            </Typography>
          </Tooltip>
        )}
        {levelChip(entry.level)}
        <Typography variant="body2" sx={{ flexGrow: 1, wordBreak: 'break-word' }}>
          {entry.message}
        </Typography>
        {hasDetails && (
          <IconButton
            size="small"
            onClick={() => setExpanded((v) => !v)}
            aria-label={expanded ? 'Collapse details' : 'Expand details'}
          >
            {expanded ? <ExpandMoreIcon fontSize="small" /> : <ChevronRightIcon fontSize="small" />}
          </IconButton>
        )}
      </Stack>
      {hasDetails && (
        <Collapse in={expanded} timeout="auto" unmountOnExit>
          <Box
            sx={{
              mt: 0.5,
              ml: 11,
              p: 1,
              bgcolor: 'grey.900',
              color: 'grey.100',
              borderRadius: 1,
              fontFamily: 'monospace',
              fontSize: '0.75rem',
              whiteSpace: 'pre-wrap',
              wordBreak: 'break-all',
            }}
          >
            {entry.details}
          </Box>
        </Collapse>
      )}
    </Box>
  );
}

/**
 * OperationActivityPanel — scoped view of the activity-feed entries for a
 * single operation, backed by GET /api/v1/operations/:id/activity, or — when
 * memberIds is given — the merged timeline of a group of operations, backed
 * by POST /api/v1/operations/activity/merged. Shows a status banner (sourced
 * from the operations store when available, else inferred from the last
 * entry's level) plus a chronological list of entries with color-coded level
 * badges and collapsible details.
 */
export function OperationActivityPanel({ operationId, memberIds, limit }: OperationActivityPanelProps) {
  const [entries, setEntries] = useState<OperationActivityEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [total, setTotal] = useState(0);
  const [truncated, setTruncated] = useState(false);
  const isUnmountedRef = useRef(false);
  const { toast } = useToast();

  const isGroup = memberIds !== undefined && memberIds.length > 0;
  // A group row lives only in groupedOperations; a real op is in both, and
  // activeOperations is the one every other consumer reads.
  const op = useOperationsStore((state) =>
    isGroup
      ? state.groupedOperations.find((o) => o.id === operationId)
      : state.activeOperations.find((o) => o.id === operationId)
  );
  const latestLogEvent = useOperationsStore((state) => state.latestLogEvent);

  const opRef = useRef(op);
  const lastAppendedSequenceRef = useRef<number | null>(null);

  useEffect(() => {
    return () => {
      isUnmountedRef.current = true;
    };
  }, []);

  useEffect(() => {
    opRef.current = op;
  }, [op]);

  // memberIds is an array prop, so a caller re-rendering with an equal but new
  // array must not refetch: key the effect on its contents, not its identity.
  const memberKey = isGroup ? memberIds.join(',') : '';

  const load = useCallback(async () => {
    if (isUnmountedRef.current) return;
    setLoading(true);
    setError(null);
    try {
      if (memberKey !== '') {
        const data = await fetchMergedOperationActivity(memberKey.split(','), limit ?? MERGED_DEFAULT_LIMIT);
        if (!isUnmountedRef.current) {
          setEntries(data.entries ?? []);
          setTotal(data.total ?? data.entries?.length ?? 0);
          setTruncated(Boolean(data.truncated));
        }
      } else {
        const data = await fetchOperationActivity(operationId, limit ?? 100);
        if (!isUnmountedRef.current) {
          setEntries(data.entries ?? []);
          setTotal(data.total ?? data.entries?.length ?? 0);
          setTruncated(false);
        }
      }
    } catch (err) {
      if (!isUnmountedRef.current) {
        setError(err instanceof Error ? err.message : String(err));
        setEntries([]);
        setTotal(0);
        setTruncated(false);
      }
    } finally {
      if (!isUnmountedRef.current) {
        setLoading(false);
      }
    }
  }, [operationId, memberKey, limit]);

  useEffect(() => {
    load();
  }, [load]);

  // Live log lines are appended from SSE. The refresh button is the explicit
  // full reload path; no timer should repaint the log while a user is reading.
  useEffect(() => {
    if (!latestLogEvent) return;
    // A merged view takes lines from any member; a single view from its op.
    const mine =
      memberKey !== '' ? memberKey.split(',').includes(latestLogEvent.op_id) : latestLogEvent.op_id === operationId;
    if (!mine) return;
    // Guards against re-appending the same SSE event when this effect re-runs
    // for an unrelated reason. `sequence` is a monotonic counter the store
    // stamps on every real log event, so it is a reliable identity even across
    // renders that hand back an equal-looking but structurally new event object.
    if (lastAppendedSequenceRef.current === latestLogEvent.sequence) return;
    lastAppendedSequenceRef.current = latestLogEvent.sequence;
    const cap = limit ?? (memberKey !== '' ? MERGED_DEFAULT_LIMIT : 100);
    const currentOp = opRef.current;
    setEntries((prev) => {
      const next = [
        ...prev,
        {
          timestamp: latestLogEvent.created_at,
          level: latestLogEvent.level,
          operation_id: latestLogEvent.op_id,
          operation_type: currentOp?.def_id ?? currentOp?.type ?? '',
          message: latestLogEvent.message,
        },
      ];
      return next.length > cap ? next.slice(next.length - cap) : next;
    });
    setTotal((prev) => prev + 1);
  }, [latestLogEvent, operationId, memberKey, limit]);

  // Plain-text representation of the log for clipboard copy.
  const logsAsText = useMemo(() => {
    return entries
      .map((e) => {
        const ts = e.timestamp;
        const lvl = (e.level || '').toUpperCase();
        const main = `${ts} ${lvl} ${e.message}`;
        return e.details && e.details.trim().length > 0 ? `${main}\n${e.details}` : main;
      })
      .join('\n');
  }, [entries]);

  const handleCopy = useCallback(async () => {
    try {
      await navigator.clipboard.writeText(logsAsText);
      toast('Copied to clipboard', 'success');
    } catch (err) {
      toast(err instanceof Error ? `Copy failed: ${err.message}` : 'Copy failed', 'error');
    }
  }, [logsAsText, toast]);

  const status = op?.status ?? inferStatusFromEntries(entries);
  const operationType = op?.displayName || op?.def_id || entries[0]?.operation_type || 'operation';
  const memberCount = isGroup ? memberIds.length : 0;

  return (
    <Paper variant="outlined" sx={{ overflow: 'hidden' }}>
      {/* Status banner */}
      <Box
        sx={{
          px: 2,
          py: 1.25,
          borderBottom: '1px solid',
          borderColor: 'divider',
          bgcolor: 'action.hover',
        }}
      >
        <Stack
          direction="row"
          spacing={1.5}
          sx={{
            alignItems: 'center',
          }}
        >
          <Typography variant="subtitle2" sx={{ fontWeight: 600 }}>
            {operationType}
            {isGroup ? ` ×${memberCount}` : ''}
          </Typography>
          <Chip
            size="small"
            label={status === 'queued' ? 'pending' : status}
            color={statusColor(status)}
          />
          <Typography
            variant="caption"
            sx={{
              color: 'text.secondary',
              fontFamily: 'monospace',
            }}
          >
            {/* A group id names no record; say what the view is instead. */}
            {isGroup ? `${memberCount} runs, merged` : operationId.slice(0, 12)}
          </Typography>
          <Box sx={{ flexGrow: 1 }} />
          <Typography
            variant="caption"
            sx={{
              color: 'text.secondary',
            }}
          >
            {truncated
              ? `showing the last ${entries.length} of ${total} entries`
              : `${total} ${total === 1 ? 'entry' : 'entries'}`}
          </Typography>
          <Tooltip title="Copy log to clipboard">
            <span>
              <IconButton
                size="small"
                onClick={handleCopy}
                aria-label="Copy log to clipboard"
                disabled={entries.length === 0}
              >
                <ContentCopyIcon fontSize="small" />
              </IconButton>
            </span>
          </Tooltip>
          {/* The download is one op's log file; a group has no single file. */}
          {!isGroup && (
            <Button
              component="a"
              href={`/api/v1/operations/v2/${encodeURIComponent(operationId)}/logs/download`}
              download
              size="small"
              sx={{ textTransform: 'none' }}
            >
              Download full log (.gz)
            </Button>
          )}
          <Tooltip title="Refresh activity">
            <IconButton size="small" onClick={load} aria-label="Refresh activity">
              <RefreshIcon fontSize="small" />
            </IconButton>
          </Tooltip>
        </Stack>
      </Box>

      {/* Body */}
      {loading && entries.length === 0 ? (
        <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}>
          <CircularProgress size={28} />
        </Box>
      ) : error ? (
        <Box sx={{ py: 3, px: 2, textAlign: 'center' }}>
          <Typography variant="body2" color="error">
            {error}
          </Typography>
        </Box>
      ) : entries.length === 0 ? (
        <Typography
          variant="body2"
          sx={{
            color: 'text.secondary',
            py: 4,
            textAlign: 'center',
          }}
        >
          No activity recorded for this operation yet.
        </Typography>
      ) : (
        <Box sx={{ maxHeight: 480, overflowY: 'auto' }}>
          {entries.map((entry, idx) => (
            <EntryRow
              key={`${entry.timestamp}-${idx}`}
              entry={entry}
              memberLabel={isGroup ? shortId(entry.operation_id) : undefined}
            />
          ))}
        </Box>
      )}
    </Paper>
  );
}

export default OperationActivityPanel;
