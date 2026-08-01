// file: web/src/pages/ReviewQueue.tsx
// version: 2.1.0
// guid: 4c8f2a17-5e93-4d60-a1b8-7f3c6d9e0a52
// last-edited: 2026-08-01

import { useEffect, useMemo, useState } from 'react';
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Avatar,
  Box,
  Button,
  Chip,
  CircularProgress,
  Divider,
  Paper,
  Stack,
  Tooltip,
  Typography,
  type SxProps,
  type Theme,
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore.js';
import CheckIcon from '@mui/icons-material/Check.js';
import CloseIcon from '@mui/icons-material/Close.js';
import AlbumIcon from '@mui/icons-material/Album.js';
import * as api from '../services/api';
import { type Book, type ReviewItem } from '../services/api';
import { useReviewStore } from '../stores/useReviewStore';
import { useAppStore } from '../stores/useAppStore';
import { labelForKind } from '../lib/reviewKinds';
import {
  type MemberEntry,
  memberCount,
  memberEntries,
  parsePayload,
} from '../lib/reviewPayload';
import { formatBytes, formatDuration } from '../utils/mediaFormat';
import { formatPath, usePathVars } from '../utils/formatPath';

// fetchBooksByIds resolves member book IDs to full Book records in bounded batches
// (a group can hold dozens of files; don't fire dozens of concurrent requests). A
// member that was hard-deleted since the hold was created simply resolves to
// undefined — the row still renders from its file path.
async function fetchBooksByIds(
  ids: string[],
  signal: AbortSignal
): Promise<Map<string, Book>> {
  const out = new Map<string, Book>();
  const unique = Array.from(new Set(ids.filter((id) => id)));
  const BATCH = 6;
  for (let i = 0; i < unique.length; i += BATCH) {
    if (signal.aborted) break;
    const slice = unique.slice(i, i + BATCH);
    const results = await Promise.allSettled(slice.map((id) => api.getBook(id)));
    results.forEach((r, j) => {
      if (r.status === 'fulfilled' && r.value) out.set(slice[j], r.value);
    });
  }
  return out;
}

// MemberRow renders one member of a review group with as much metadata as we could
// resolve — cover, title, author, series, format/duration/size/bitrate chips, and the
// proposed disc/track order. When the source book couldn't be fetched (e.g. it was
// hard-deleted since the hold was created) it degrades to the bare file path.
function MemberRow({
  entry,
  book,
  pathVars,
}: {
  entry: MemberEntry;
  book: Book | undefined;
  pathVars: ReturnType<typeof usePathVars>;
}) {
  const title = book?.title || entry.filePath.split('/').pop() || '(unknown)';
  const size = book?.file_size;
  const duration = book?.duration;
  const bitrate = book?.bitrate;
  const codec = book?.codec;
  const hasDisc = typeof entry.disc === 'number' && entry.disc > 0;
  const hasTrack = typeof entry.track === 'number' && entry.track > 0;

  return (
    <Box
      sx={{
        display: 'flex',
        gap: 1.25,
        p: 1,
        borderRadius: 1,
        bgcolor: 'action.hover',
        alignItems: 'flex-start',
        minWidth: 0,
      }}
    >
      <Avatar
        variant="rounded"
        src={book?.cover_url || book?.cover_image || undefined}
        sx={{ width: 40, height: 40, bgcolor: 'action.selected' }}
      >
        <AlbumIcon fontSize="small" />
      </Avatar>
      <Box sx={{ minWidth: 0, flex: 1 }}>
        <Typography variant="body2" fontWeight={600} noWrap title={title}>
          {title}
        </Typography>
        {(book?.author_name || book?.series_name) && (
          <Typography variant="caption" color="text.secondary" noWrap display="block">
            {book?.author_name}
            {book?.series_name && (
              <>
                {book?.author_name ? ' · ' : ''}
                {book.series_name}
                {book.series_position != null ? ` #${book.series_position}` : ''}
              </>
            )}
          </Typography>
        )}
        <Stack direction="row" spacing={0.5} sx={{ mt: 0.5 }} flexWrap="wrap" useFlexGap>
          {(hasDisc || hasTrack) && (
            <Chip
              size="small"
              color="primary"
              variant="outlined"
              label={`${hasDisc ? `Disc ${entry.disc} · ` : ''}Track ${hasTrack ? entry.track : '?'}`}
            />
          )}
          {book?.format && <Chip size="small" label={book.format.toUpperCase()} />}
          {duration != null && (
            <Chip size="small" variant="outlined" label={formatDuration(duration)} />
          )}
          {size != null && <Chip size="small" variant="outlined" label={formatBytes(size)} />}
          {bitrate != null && (
            <Chip size="small" variant="outlined" label={`${bitrate}kbps`} />
          )}
          {codec && <Chip size="small" variant="outlined" label={codec} />}
        </Stack>
        {entry.filePath && (
          <Tooltip title={entry.filePath} placement="bottom-start"
            componentsProps={{ tooltip: { sx: { maxWidth: 600 } } }}>
            <Typography
              variant="caption"
              sx={{ fontFamily: 'monospace', fontSize: '0.65rem', display: 'block', mt: 0.5 }}
              noWrap
            >
              {formatPath(entry.filePath, pathVars)}
            </Typography>
          </Tooltip>
        )}
      </Box>
    </Box>
  );
}

// MemberFilesDetail renders a review item's payload header plus a rich per-member
// list. It fetches the member books lazily (it only mounts when the accordion is
// expanded, via unmountOnExit) so opening the queue never fans out hundreds of
// getBook calls up front.
function MemberFilesDetail({ item }: { item: ReviewItem }) {
  const pathVars = usePathVars();
  const payload = useMemo(() => parsePayload(item.payload), [item.payload]);
  const folder = payload?.folder ?? item.folder_ref;
  const proposed = payload?.proposedAction ?? payload?.proposed_action;
  const title = payload?.survivorTitle ?? payload?.derived_title ?? payload?.title;
  const confidence =
    typeof payload?.confidence === 'string'
      ? payload.confidence
      : typeof payload?.confidence === 'number'
        ? payload.confidence.toFixed(2)
        : undefined;
  const entries = useMemo(() => memberEntries(payload), [payload]);
  const members = memberCount(payload);

  const [books, setBooks] = useState<Map<string, Book>>(new Map());
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    const ids = entries.map((e) => e.bookId).filter((id): id is string => !!id);
    if (ids.length === 0) return;
    const ctrl = new AbortController();
    setLoading(true);
    fetchBooksByIds(ids, ctrl.signal)
      .then((m) => {
        if (!ctrl.signal.aborted) setBooks(m);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });
    return () => ctrl.abort();
  }, [entries]);

  return (
    <Box sx={{ pl: 1 }}>
      <Stack spacing={0.5} sx={{ mb: 1 }}>
        {folder && (
          <Typography variant="body2">
            <strong>Folder:</strong> <code>{folder}</code>
          </Typography>
        )}
        {title && (
          <Typography variant="body2">
            <strong>Proposed title:</strong> {title}
          </Typography>
        )}
        {proposed && (
          <Typography variant="body2">
            <strong>Proposed action:</strong> {proposed}
          </Typography>
        )}
        <Stack direction="row" spacing={1} alignItems="center">
          {members !== undefined && (
            <Typography variant="body2">
              <strong>Members:</strong> {members} file{members === 1 ? '' : 's'}
            </Typography>
          )}
          {confidence && (
            <Chip
              size="small"
              label={confidence === 'high' ? 'High confidence' : confidence}
              color={confidence === 'high' ? 'success' : 'default'}
              variant="outlined"
            />
          )}
          {loading && <CircularProgress size={14} />}
        </Stack>
      </Stack>
      {entries.length > 0 && (
        <Stack spacing={0.75}>
          {entries.map((e, i) => (
            <MemberRow
              key={e.bookId ?? e.filePath ?? i}
              entry={e}
              book={e.bookId ? books.get(e.bookId) : undefined}
              pathVars={pathVars}
            />
          ))}
        </Stack>
      )}
    </Box>
  );
}

export function ReviewQueue() {
  const items = useReviewStore((s) => s.items);
  const itemsLoading = useReviewStore((s) => s.itemsLoading);
  const loadItems = useReviewStore((s) => s.loadItems);
  const loadCount = useReviewStore((s) => s.loadCount);
  const addNotification = useAppStore((s) => s.addNotification);

  // Track in-flight action ids/kinds so buttons disable while their request runs.
  const [busyItems, setBusyItems] = useState<Set<string>>(new Set());
  const [busyKinds, setBusyKinds] = useState<Set<string>>(new Set());

  useEffect(() => {
    void loadItems({ status: 'pending' });
  }, [loadItems]);

  // Refresh both the queue and the badge count after any action.
  const refresh = async () => {
    await Promise.all([loadItems({ status: 'pending' }), loadCount()]);
  };

  // Bucket pending items by Kind, preserving a stable label + count per bucket.
  const buckets = useMemo(() => {
    const map = new Map<string, ReviewItem[]>();
    for (const item of items) {
      const list = map.get(item.kind) ?? [];
      list.push(item);
      map.set(item.kind, list);
    }
    return Array.from(map.entries())
      .map(([kind, kindItems]) => ({ kind, label: labelForKind(kind), items: kindItems }))
      .sort((a, b) => a.label.localeCompare(b.label));
  }, [items]);

  const handleItemAction = async (item: ReviewItem, action: 'approve' | 'reject') => {
    setBusyItems((prev) => new Set(prev).add(item.id));
    try {
      if (action === 'approve') {
        await api.approveReviewItem(item.id);
      } else {
        await api.rejectReviewItem(item.id);
      }
      await refresh();
    } catch (err) {
      addNotification(
        `Failed to ${action} item: ${err instanceof Error ? err.message : 'unknown error'}`,
        'error'
      );
    } finally {
      setBusyItems((prev) => {
        const next = new Set(prev);
        next.delete(item.id);
        return next;
      });
    }
  };

  const handleBulkAction = async (kind: string, action: 'approve' | 'reject') => {
    setBusyKinds((prev) => new Set(prev).add(kind));
    try {
      const result = await api.bulkReviewAction({ action, kind });
      addNotification(
        `${action === 'approve' ? 'Approved' : 'Rejected'} ${result.processed} item${
          result.processed === 1 ? '' : 's'
        }`,
        'success'
      );
      await refresh();
    } catch (err) {
      addNotification(
        `Bulk ${action} failed: ${err instanceof Error ? err.message : 'unknown error'}`,
        'error'
      );
    } finally {
      setBusyKinds((prev) => {
        const next = new Set(prev);
        next.delete(kind);
        return next;
      });
    }
  };

  // Approve/Reject pair for a single item. Defined here rather than at module
  // scope so it closes over handleItemAction; rendered twice per item (above and
  // below the member-file list) so the decision is reachable without scrolling
  // past a long multi-disc listing.
  const ItemActions = ({
    item,
    busy,
    sx,
  }: {
    item: ReviewItem;
    busy: boolean;
    sx?: SxProps<Theme>;
  }) => (
    <Stack direction="row" spacing={1} sx={sx}>
      <Button
        size="small"
        variant="contained"
        color="success"
        startIcon={<CheckIcon />}
        disabled={busy}
        onClick={() => handleItemAction(item, 'approve')}
      >
        Approve
      </Button>
      <Button
        size="small"
        variant="outlined"
        color="error"
        startIcon={<CloseIcon />}
        disabled={busy}
        onClick={() => handleItemAction(item, 'reject')}
      >
        Reject
      </Button>
    </Stack>
  );

  return (
    <Box>
      <Typography variant="h4" gutterBottom>
        Review Queue
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
        Items the system has flagged for a human decision, grouped by type. Approve or reject a
        whole group, or expand a group to act on individual items.
      </Typography>

      {itemsLoading && items.length === 0 ? (
        <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}>
          <CircularProgress />
        </Box>
      ) : buckets.length === 0 ? (
        <Paper sx={{ p: 6, textAlign: 'center' }}>
          <Typography variant="h6" gutterBottom>
            Nothing to review 🎉
          </Typography>
          <Typography variant="body2" color="text.secondary">
            When the system flags something for a decision, it will show up here.
          </Typography>
        </Paper>
      ) : (
        <Stack spacing={2}>
          {buckets.map((bucket) => {
            const kindBusy = busyKinds.has(bucket.kind);
            return (
              <Paper key={bucket.kind} sx={{ p: 2 }}>
                <Box
                  sx={{
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    flexWrap: 'wrap',
                    gap: 1,
                    mb: 1,
                  }}
                >
                  <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                    <Typography variant="h6">{bucket.label}</Typography>
                    <Chip size="small" label={bucket.items.length} />
                  </Box>
                  <Stack direction="row" spacing={1}>
                    <Button
                      size="small"
                      variant="contained"
                      color="success"
                      startIcon={<CheckIcon />}
                      disabled={kindBusy}
                      onClick={() => handleBulkAction(bucket.kind, 'approve')}
                    >
                      Approve all
                    </Button>
                    <Button
                      size="small"
                      variant="outlined"
                      color="error"
                      startIcon={<CloseIcon />}
                      disabled={kindBusy}
                      onClick={() => handleBulkAction(bucket.kind, 'reject')}
                    >
                      Reject all
                    </Button>
                  </Stack>
                </Box>
                <Divider sx={{ mb: 1 }} />
                {bucket.items.map((item) => {
                  const itemBusy = busyItems.has(item.id);
                  return (
                    <Accordion
                      key={item.id}
                      disableGutters
                      slotProps={{ transition: { unmountOnExit: true } }}
                    >
                      <AccordionSummary expandIcon={<ExpandMoreIcon />}>
                        <Typography variant="body2" sx={{ flexGrow: 1 }}>
                          {item.summary || item.folder_ref || item.id}
                        </Typography>
                      </AccordionSummary>
                      <AccordionDetails>
                        {/*
                          Actions are rendered ABOVE the detail as well as below it.
                          A multi-disc item can list dozens of member files, so with
                          the buttons only at the bottom you had to scroll the whole
                          list to act on an item you had already decided about after
                          reading the first line. Repeating them costs one row and
                          means the decision is always reachable from wherever you
                          are — the top pair for a quick call, the bottom pair for
                          when you have actually read to the end.
                        */}
                        <ItemActions item={item} busy={itemBusy} sx={{ mb: 2 }} />
                        <MemberFilesDetail item={item} />
                        <ItemActions item={item} busy={itemBusy} sx={{ mt: 2 }} />
                      </AccordionDetails>
                    </Accordion>
                  );
                })}
              </Paper>
            );
          })}
        </Stack>
      )}
    </Box>
  );
}

export default ReviewQueue;
