// file: web/src/pages/ReviewQueue.tsx
// version: 1.0.0
// guid: 4c8f2a17-5e93-4d60-a1b8-7f3c6d9e0a52
// last-edited: 2026-07-13

import { useEffect, useMemo, useState } from 'react';
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Box,
  Button,
  Chip,
  CircularProgress,
  Divider,
  List,
  ListItem,
  Paper,
  Stack,
  Typography,
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore.js';
import CheckIcon from '@mui/icons-material/Check.js';
import CloseIcon from '@mui/icons-material/Close.js';
import * as api from '../services/api';
import { type ReviewItem } from '../services/api';
import { useReviewStore } from '../stores/useReviewStore';
import { useAppStore } from '../stores/useAppStore';
import { labelForKind } from '../lib/reviewKinds';

// A defensively-typed view of a review item's JSON payload. The producer op
// (Track B) is not built yet, so NOTHING writes this shape in the current repo
// state — every field is optional and parsing is wrapped in try/catch. Render
// only what is present; never assume a key exists.
interface ReviewPayload {
  folder?: string;
  proposed_action?: string;
  action?: string;
  derived_title?: string;
  title?: string;
  member_ids?: string[];
  member_count?: number;
  confidence?: number;
  files?: string[];
  [k: string]: unknown;
}

function parsePayload(raw: string): ReviewPayload | null {
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === 'object' ? (parsed as ReviewPayload) : null;
  } catch {
    return null;
  }
}

function memberCount(payload: ReviewPayload | null): number | undefined {
  if (!payload) return undefined;
  if (typeof payload.member_count === 'number') return payload.member_count;
  if (Array.isArray(payload.member_ids)) return payload.member_ids.length;
  if (Array.isArray(payload.files)) return payload.files.length;
  return undefined;
}

function PayloadDetails({ item }: { item: ReviewItem }) {
  const payload = parsePayload(item.payload);
  const folder = payload?.folder ?? item.folder_ref;
  const proposed = payload?.proposed_action ?? payload?.action;
  const title = payload?.derived_title ?? payload?.title;
  const members = memberCount(payload);
  const confidence = typeof payload?.confidence === 'number' ? payload.confidence : undefined;
  const files = Array.isArray(payload?.files) ? payload!.files! : undefined;

  return (
    <Box sx={{ pl: 1 }}>
      <Stack spacing={0.5} sx={{ mb: files ? 1 : 0 }}>
        {folder && (
          <Typography variant="body2">
            <strong>Folder:</strong> <code>{folder}</code>
          </Typography>
        )}
        {title && (
          <Typography variant="body2">
            <strong>Derived title:</strong> {title}
          </Typography>
        )}
        {proposed && (
          <Typography variant="body2">
            <strong>Proposed action:</strong> {proposed}
          </Typography>
        )}
        {members !== undefined && (
          <Typography variant="body2">
            <strong>Members:</strong> {members} file{members === 1 ? '' : 's'}
          </Typography>
        )}
        {confidence !== undefined && (
          <Typography variant="body2">
            <strong>Confidence:</strong> {confidence.toFixed(2)}
          </Typography>
        )}
      </Stack>
      {files && files.length > 0 && (
        <Box>
          <Typography variant="caption" color="text.secondary">
            Files
          </Typography>
          <List dense disablePadding sx={{ pl: 1 }}>
            {files.slice(0, 25).map((f, i) => (
              <ListItem key={i} disableGutters sx={{ py: 0 }}>
                <Typography variant="caption" sx={{ wordBreak: 'break-all' }}>
                  {f}
                </Typography>
              </ListItem>
            ))}
            {files.length > 25 && (
              <ListItem disableGutters sx={{ py: 0 }}>
                <Typography variant="caption" color="text.secondary">
                  …and {files.length - 25} more
                </Typography>
              </ListItem>
            )}
          </List>
        </Box>
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
                    <Accordion key={item.id} disableGutters>
                      <AccordionSummary expandIcon={<ExpandMoreIcon />}>
                        <Typography variant="body2" sx={{ flexGrow: 1 }}>
                          {item.summary || item.folder_ref || item.id}
                        </Typography>
                      </AccordionSummary>
                      <AccordionDetails>
                        <PayloadDetails item={item} />
                        <Stack direction="row" spacing={1} sx={{ mt: 2 }}>
                          <Button
                            size="small"
                            variant="contained"
                            color="success"
                            startIcon={<CheckIcon />}
                            disabled={itemBusy}
                            onClick={() => handleItemAction(item, 'approve')}
                          >
                            Approve
                          </Button>
                          <Button
                            size="small"
                            variant="outlined"
                            color="error"
                            startIcon={<CloseIcon />}
                            disabled={itemBusy}
                            onClick={() => handleItemAction(item, 'reject')}
                          >
                            Reject
                          </Button>
                        </Stack>
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
