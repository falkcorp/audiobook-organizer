// file: web/src/pages/ReviewQueue.tsx
// version: 2.3.0
// guid: 4c8f2a17-5e93-4d60-a1b8-7f3c6d9e0a52
// last-edited: 2026-08-10
import { useEffect, useMemo, useState } from 'react';
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Alert,
  AlertTitle,
  Avatar,
  Box,
  Button,
  Chip,
  CircularProgress,
  Divider,
  MenuItem,
  Paper,
  Stack,
  TextField,
  Tooltip,
  Typography,
  type SxProps,
  type Theme,
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import CheckIcon from '@mui/icons-material/Check';
import CloseIcon from '@mui/icons-material/Close';
import AlbumIcon from '@mui/icons-material/Album';
import HelpOutlineIcon from '@mui/icons-material/HelpOutline';
import * as api from '../services/api';
import { type Book, type ReviewItem } from '../services/api';
import { useReviewStore } from '../stores/useReviewStore';
import { useAppStore } from '../stores/useAppStore';
import { labelForKind } from '../lib/reviewKinds';
import {
  REVIEW_ACTIONS,
  actionSpec,
  defaultActionFor,
  evidenceFacts,
  labelForAction,
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

// RecommendationPanel is the part of a hold a reviewer actually reads before
// deciding: what the classifier recommends, the sentence explaining why, and the
// NUMBERS that sentence is built from.
//
// 🔴 THE NUMBERS ARE THE POINT. Before recommendations, 762 of 777 holds carried the
// identical `proposedAction` string, which is a queue nobody can work. A reason
// without its evidence would just be a nicer generic string — the member count, how
// many runtimes are actually KNOWN, how many members are book-length, and the
// median/longest runtime are what let a reviewer check the machine rather than
// trust it. The known-vs-total runtime chip is highlighted when most runtimes are
// missing, because that gap is exactly why a recommendation lands on
// "insufficient evidence".
function RecommendationPanel({
  recommended,
  reason,
  facts,
}: {
  recommended: string | undefined;
  reason: string | undefined;
  facts: ReturnType<typeof evidenceFacts>;
}) {
  const spec = actionSpec(recommended);
  const undecidable = !spec || !spec.approvable;

  return (
    <Alert
      severity={undecidable ? 'warning' : 'info'}
      icon={undecidable ? <HelpOutlineIcon fontSize="inherit" /> : false}
      sx={{ mb: 2, '& .MuiAlert-message': { width: '100%' } }}
    >
      <AlertTitle sx={{ mb: 0.5 }}>
        {undecidable
          ? 'The classifier cannot tell — your decision is required'
          : `Recommended: ${spec.label}`}
      </AlertTitle>
      {reason && (
        <Typography variant="body2" sx={{ mb: facts.length ? 1 : 0 }}>
          {reason}
        </Typography>
      )}
      {!reason && (
        <Typography variant="body2" color="text.secondary" sx={{ mb: facts.length ? 1 : 0 }}>
          This hold predates evidence-backed recommendations. Re-run the regroup scan to
          refresh it, or decide it here.
        </Typography>
      )}
      {facts.length > 0 ? (
        <Stack direction="row" spacing={0.5} flexWrap="wrap" useFlexGap>
          {facts.map((f) => (
            <Tooltip key={f.label} title={f.hint} placement="top">
              <Chip
                size="small"
                label={f.label}
                color={f.warn ? 'warning' : 'default'}
                variant={f.warn ? 'filled' : 'outlined'}
              />
            </Tooltip>
          ))}
        </Stack>
      ) : (
        <Typography variant="caption" color="text.secondary">
          No evidence recorded for this hold.
        </Typography>
      )}
    </Alert>
  );
}

// ActionSelector lets a reviewer OVERRIDE the recommendation per item. The chosen
// action is sent as {"action": "..."} and persisted on the hold, so it survives to
// the replay that does the real work once review_apply_enabled is on.
//
// 🔴 `insufficient-evidence` IS NOT AN OPTION. The backend 400s it deliberately — it
// is a statement BY the machine, not a decision a human can take — so offering it
// would be offering a button that always fails. Holds recommending it simply start
// with nothing selected and Approve stays disabled until a real choice is made,
// rather than pre-filling a guess (which would be `combine` on precisely the holds
// with the least evidence).
function ActionSelector({
  value,
  onChange,
  disabled,
}: {
  value: string;
  onChange: (v: string) => void;
  disabled: boolean;
}) {
  const chosen = actionSpec(value);
  return (
    <Stack spacing={0.5} sx={{ minWidth: 260 }}>
      <TextField
        select
        size="small"
        label="Action"
        value={value}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value)}
        helperText={chosen?.description ?? 'Choose what should happen to these files.'}
      >
        <MenuItem value="">
          <em>Choose an action…</em>
        </MenuItem>
        {REVIEW_ACTIONS.filter((a) => a.approvable).map((a) => (
          <MenuItem key={a.value} value={a.value}>
            {a.label}
            {a.unimplemented && ' (not implemented)'}
          </MenuItem>
        ))}
      </TextField>
    </Stack>
  );
}

// ItemActions is the action selector plus the Approve/Reject pair for one hold.
//
// Rendered TWICE per item — above and below the member-file list — because a
// multi-disc hold can list dozens of files, and with the buttons only at the bottom
// you had to scroll the whole listing to act on something you decided about after
// reading the first line. Both copies read the same resolved `action` prop, so
// changing the selector at the top is exactly what the bottom Approve sends.
//
// 🔴 Module scope, not inline in ReviewQueue. A component declared inside the parent
// render is a NEW component type on every render, so React unmounts and remounts its
// subtree — which would slam the action dropdown shut whenever anything else on the
// page updated. The buttons this replaced were stateless and got away with it; a
// select is not.
//
// Approve is DISABLED with no action selected. That is the whole insufficient-evidence
// story on the client: those holds start empty (defaultActionFor) and stay
// un-approvable until a human names something, matching the backend's deliberate 400.
function ItemActions({
  item,
  action,
  busy,
  withSelector,
  onApprove,
  onReject,
  onActionChange,
  sx,
}: {
  item: ReviewItem;
  action: string;
  busy: boolean;
  /** The selector is rendered once (top). The bottom copy shows the resolved action
   *  read-only, so a reviewer who scrolled past a long listing can still see what
   *  Approve is about to do without a second control drifting out of sync. */
  withSelector?: boolean;
  onApprove: (item: ReviewItem) => void;
  onReject: (item: ReviewItem) => void;
  onActionChange: (id: string, value: string) => void;
  sx?: SxProps<Theme>;
}) {
  const spec = actionSpec(action);
  return (
    <Stack direction="row" spacing={1.5} alignItems="flex-start" flexWrap="wrap" useFlexGap sx={sx}>
      {withSelector ? (
        <ActionSelector
          value={action}
          disabled={busy}
          onChange={(v) => onActionChange(item.id, v)}
        />
      ) : (
        action && (
          <Chip
            size="small"
            variant="outlined"
            color={spec?.destructive ? 'warning' : 'default'}
            label={`Will ${labelForAction(action).toLowerCase()}`}
          />
        )
      )}
      <Stack direction="row" spacing={1} sx={[withSelector ? {
        mt: 0.5
      } : {
        mt: 0
      }]}>
        <Tooltip
          title={
            action
              ? ''
              : 'Pick an action first — this hold has no recommendation the system is willing to act on.'
          }
        >
          {/* span so the Tooltip still fires on a disabled Button */}
          <span>
            <Button
              size="small"
              variant="contained"
              color={spec?.destructive ? 'warning' : 'success'}
              startIcon={<CheckIcon />}
              disabled={busy || !action}
              onClick={() => onApprove(item)}
            >
              Approve
            </Button>
          </span>
        </Tooltip>
        <Button
          size="small"
          variant="outlined"
          color="error"
          startIcon={<CloseIcon />}
          disabled={busy}
          onClick={() => onReject(item)}
        >
          Reject
        </Button>
      </Stack>
    </Stack>
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

  // Per-item chosen action. Keyed by item id and populated lazily: an id absent from
  // this map has not been touched by the reviewer, so it falls back to the hold's own
  // recommendation (or to "" when that recommendation is not approvable). Keeping the
  // overrides here rather than in each row means the two ItemActions rendered per item
  // — above and below the file list — always agree about what Approve will send.
  const [chosenActions, setChosenActions] = useState<Record<string, string>>({});

  // The last bulk run's skipped items, kept on screen until the next bulk action.
  // A toast is the wrong home for these: the whole point is that a reviewer can go
  // deal with them, which means the ids have to stay readable.
  const [bulkSkips, setBulkSkips] = useState<
    { kind: string; skipped: api.ReviewBulkSkip[] } | null
  >(null);

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
                  <Alert
                    severity="warning"
                    sx={{ mb: 1 }}
                    onClose={() => setBulkSkips(null)}
                  >
                    <AlertTitle>
                      {bulkSkips.skipped.length} hold
                      {bulkSkips.skipped.length === 1 ? ' was' : 's were'} skipped — still
                      pending
                    </AlertTitle>
                    <Typography variant="body2" sx={{ mb: 1 }}>
                      Bulk approve uses each hold&apos;s own recommendation. These had none it
                      would act on, so they were left alone. Open them below and choose an
                      action.
                    </Typography>
                    <Stack spacing={0.5}>
                      {bulkSkips.skipped.map((s) => (
                        <Typography key={s.id} variant="caption" display="block">
                          <code>{s.id}</code>
                          {s.action ? ` · ${labelForAction(s.action) || s.action}` : ''} — {s.reason}
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
                          alignItems="center"
                          sx={{ flexGrow: 1, minWidth: 0 }}
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
                          facts={evidenceFacts(payload?.recommendationEvidence)}
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
