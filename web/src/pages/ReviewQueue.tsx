// file: web/src/pages/ReviewQueue.tsx
// version: 2.5.0
// guid: 4c8f2a17-5e93-4d60-a1b8-7f3c6d9e0a52
// last-edited: 2026-08-20
import { useEffect, useMemo, useState } from 'react';
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Alert,
  AlertTitle,
  Box,
  Button,
  Chip,
  CircularProgress,
  Divider,
  Paper,
  Stack,
  Tooltip,
  Typography,
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import CheckIcon from '@mui/icons-material/Check';
import CloseIcon from '@mui/icons-material/Close';
import * as api from '../services/api';
import { type ReviewItem } from '../services/api';
import { useReviewStore } from '../stores/useReviewStore';
import { useAppStore } from '../stores/useAppStore';
import { labelForKind } from '../lib/reviewKinds';
import {
  actionSpec,
  defaultActionFor,
  labelForAction,
  parsePayload,
} from '../lib/reviewPayload';
import {
  ItemActions,
  MemberFilesDetail,
  RecommendationPanel,
} from '../components/review/spine/RegroupSpine';

// The presentational pieces of a hold -- MemberRow, RecommendationPanel,
// ActionSelector, ItemActions, MemberFilesDetail -- now live in the regroup
// spine and are imported above. They were moved rather than copied so this
// page and the /review lane cannot drift apart while both exist. This page is
// deleted in Phase 7; the spine is where they stay.

export function ReviewQueue() {
  const items = useReviewStore((s) => s.items);
  const itemsLoading = useReviewStore((s) => s.itemsLoading);
  const loadItems = useReviewStore((s) => s.loadItems);
  const loadCount = useReviewStore((s) => s.loadCount);
  const addNotification = useAppStore((s) => s.addNotification);

  // Track in-flight action ids/kinds so buttons disable while their request runs.
  const [busyItems, setBusyItems] = useState<Set<string>>(new Set());
  const [busyKinds, setBusyKinds] = useState<Set<string>>(new Set());

  // Per-item chosen action. Keyed by item id and populated lazily: an id absent from
  // this map has not been touched by the reviewer, so it falls back to the hold's own
  // recommendation (or to "" when that recommendation is not approvable). Keeping the
  // overrides here rather than in each row means the two ItemActions rendered per item
  // — above and below the file list — always agree about what Approve will send.
  const [chosenActions, setChosenActions] = useState<Record<string, string>>({});

  // The last bulk run's skipped items, kept on screen until the next bulk action.
  // A toast is the wrong home for these: the whole point is that a reviewer can go
  // deal with them, which means the ids have to stay readable.
  const [bulkSkips, setBulkSkips] = useState<{
    kind: string;
    skipped: api.ReviewBulkSkip[];
  } | null>(null);

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

  // actionFor resolves what Approve will send for one item: the reviewer's explicit
  // pick if they made one, else the hold's own approvable recommendation, else "" —
  // which disables Approve rather than guessing.
  const actionFor = (item: ReviewItem): string => {
    const override = chosenActions[item.id];
    if (override !== undefined) return override;
    return defaultActionFor(parsePayload(item.payload));
  };

  const handleItemAction = async (item: ReviewItem, action: 'approve' | 'reject') => {
    setBusyItems((prev) => new Set(prev).add(item.id));
    try {
      if (action === 'approve') {
        // Always send the resolved action explicitly. The backend would accept an
        // empty body and use the recommendation, but sending what the UI actually
        // displayed removes any chance of the two disagreeing.
        await api.approveReviewItem(item.id, actionFor(item));
      } else {
        await api.rejectReviewItem(item.id);
      }
      await refresh();
    } catch (err) {
      // The backend's own message is surfaced verbatim. That matters most for
      // `duplicate-of`, which answers 501 with an explanation of who owns it: a
      // generic "failed to approve" would hide a refusal the reviewer needs to read,
      // and pretending it succeeded would mark the hold decided while doing nothing.
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

  const handleApprove = (item: ReviewItem) => void handleItemAction(item, 'approve');
  const handleReject = (item: ReviewItem) => void handleItemAction(item, 'reject');
  const handleActionChange = (id: string, value: string) =>
    setChosenActions((prev) => ({ ...prev, [id]: value }));

  const handleBulkAction = async (kind: string, action: 'approve' | 'reject') => {
    setBusyKinds((prev) => new Set(prev).add(kind));
    try {
      const result = await api.bulkReviewAction({ action, kind });
      const skipped = result.skipped ?? [];
      // 🔴 REPORT THE SKIPS IN THE SAME BREATH AS THE SUCCESSES. Bulk approve runs
      // each item's OWN recommendation and refuses the undecidable ones; a message
      // quoting only `processed` would let a reviewer believe a bucket was cleared
      // when a third of it is still sitting there.
      addNotification(
        `${action === 'approve' ? 'Approved' : 'Rejected'} ${result.processed} item${
          result.processed === 1 ? '' : 's'
        }${skipped.length ? ` · ${skipped.length} skipped, listed below` : ''}`,
        skipped.length ? 'warning' : 'success'
      );
      setBulkSkips(skipped.length ? { kind, skipped } : null);
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
      <Typography
        variant="body2"
        sx={{
          color: 'text.secondary',
          mb: 3,
        }}
      >
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
          <Typography
            variant="body2"
            sx={{
              color: 'text.secondary',
            }}
          >
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
                    <Tooltip title="Approves each hold with ITS OWN recommendation — there is no batch-wide action. Holds the classifier could not decide are skipped and listed, not approved.">
                      <span>
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
                      </span>
                    </Tooltip>
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
                {bulkSkips?.kind === bucket.kind && (
                  <Alert severity="warning" sx={{ mb: 1 }} onClose={() => setBulkSkips(null)}>
                    <AlertTitle>
                      {bulkSkips.skipped.length} hold
                      {bulkSkips.skipped.length === 1 ? ' was' : 's were'} skipped — still pending
                    </AlertTitle>
                    <Typography variant="body2" sx={{ mb: 1 }}>
                      Bulk approve uses each hold&apos;s own recommendation. These had none it would
                      act on, so they were left alone. Open them below and choose an action.
                    </Typography>
                    <Stack spacing={0.5}>
                      {bulkSkips.skipped.map((s) => (
                        <Typography
                          key={s.id}
                          variant="caption"
                          sx={{
                            display: 'block',
                          }}
                        >
                          <code>{s.id}</code>
                          {s.action ? ` · ${labelForAction(s.action) || s.action}` : ''} —{' '}
                          {s.reason}
                        </Typography>
                      ))}
                    </Stack>
                  </Alert>
                )}
                <Divider sx={{ mb: 1 }} />
                {bucket.items.map((item) => {
                  const itemBusy = busyItems.has(item.id);
                  const payload = parsePayload(item.payload);
                  const action = actionFor(item);
                  const recommended = payload?.recommendedAction;
                  const recSpec = actionSpec(recommended);
                  const needsHuman = !recSpec || !recSpec.approvable;
                  return (
                    <Accordion
                      key={item.id}
                      disableGutters
                      slotProps={{ transition: { unmountOnExit: true } }}
                    >
                      <AccordionSummary expandIcon={<ExpandMoreIcon />}>
                        <Stack
                          direction="row"
                          spacing={1}
                          sx={{
                            alignItems: 'center',
                            flexGrow: 1,
                            minWidth: 0,
                          }}
                        >
                          <Typography variant="body2" noWrap sx={{ flexGrow: 1, minWidth: 0 }}>
                            {item.summary || item.folder_ref || item.id}
                          </Typography>
                          {/* The recommendation is shown COLLAPSED so a reviewer can
                              triage a bucket without opening every row. An
                              undecidable hold is flagged rather than hidden: those
                              are the ones that need a person, and they are the
                              majority of the current queue. */}
                          <Chip
                            size="small"
                            label={
                              needsHuman
                                ? 'Needs a decision'
                                : `Rec: ${labelForAction(recommended)}`
                            }
                            color={
                              needsHuman ? 'warning' : recSpec?.destructive ? 'default' : 'info'
                            }
                            variant="outlined"
                          />
                        </Stack>
                      </AccordionSummary>
                      <AccordionDetails>
                        <RecommendationPanel
                          recommended={recommended}
                          reason={payload?.recommendationReason}
                          evidence={payload?.recommendationEvidence}
                        />
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
                        <ItemActions
                          item={item}
                          action={action}
                          busy={itemBusy}
                          withSelector
                          onApprove={handleApprove}
                          onReject={handleReject}
                          onActionChange={handleActionChange}
                          sx={{ mb: 2 }}
                        />
                        <MemberFilesDetail item={item} />
                        <ItemActions
                          item={item}
                          action={action}
                          busy={itemBusy}
                          onApprove={handleApprove}
                          onReject={handleReject}
                          onActionChange={handleActionChange}
                          sx={{ mt: 2 }}
                        />
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
