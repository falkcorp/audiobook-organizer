// file: web/src/components/review/spine/RegroupSpine.tsx
// version: 1.7.0
// guid: 8c14d7e2-6b03-4a95-9f28-5e7a1c0b3d64
// last-edited: 2026-09-01

/**
 * The regroup lane's renderer -- the THIRD comparison shape in this workspace.
 *
 * CompareSpine puts a book beside a proposed set of metadata. DupesSpine puts
 * two books beside each other. Neither fits here: a regroup hold proposes a
 * GROUPING, so what a reviewer weighs is a recommendation plus the member files
 * it would apply to. That is why this is a sibling rather than a generalisation
 * of either -- the resolved answer to the question CompareSpine's header left
 * open.
 *
 * The presentational parts below were MOVED here from pages/ReviewQueue.tsx
 * rather than copied. Phase 7 deletes that page, but until it does, both
 * surfaces render these same components, so they cannot drift apart in the gap.
 */

import { memo, useEffect, useMemo, useState } from 'react';
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
import HelpOutlineIcon from '@mui/icons-material/HelpOutlineOutlined';
import * as api from '../../../services/api';
import { type Book, type PathAlias, type ReviewItem } from '../../../services/api';
import {
  REVIEW_ACTIONS,
  actionSpec,
  labelForAction,
  type MemberEntry,
  memberCount,
  memberEntries,
  type ReviewPayload,
  type RecommendationEvidence,
} from '../../../lib/reviewPayload';
import { EvidencePanel } from '../evidence/EvidencePanel';
import { regroupEvidence } from '../evidence/adapters';
import { formatBytes, formatDuration } from '../../../utils/mediaFormat';
import { PathLinks, usePathAliases } from '../../common/PathLinks';
import { usePathVars, type PathVar } from '../../../utils/formatPath';
import type { RegroupBucket, RegroupLane } from '../lanes/useRegroupLane';
import { regroupLane } from '../lanes/regroup';

// fetchBooksByIds resolves member book IDs to full Book records in bounded batches
// (a group can hold dozens of files; don't fire dozens of concurrent requests). A
// member that was hard-deleted since the hold was created simply resolves to
// undefined — the row still renders from its file path.
export async function fetchBooksByIds(
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
export function MemberRow({
  entry,
  book,
  pathAliases,
  pathVars,
}: {
  entry: MemberEntry;
  book: Book | undefined;
  pathAliases: PathAlias[];
  /** Threaded, not re-derived per row -- see the vars prop on PathLinksProps. */
  pathVars: PathVar[];
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
        <Typography
          variant="body2"
          noWrap
          title={title}
          sx={{
            fontWeight: 600,
          }}
        >
          {title}
        </Typography>
        {(book?.author_name || book?.series_name) && (
          <Typography
            variant="caption"
            noWrap
            sx={{
              color: 'text.secondary',
              display: 'block',
            }}
          >
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
        <Stack
          direction="row"
          spacing={0.5}
          useFlexGap
          sx={{
            flexWrap: 'wrap',
            mt: 0.5,
          }}
        >
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
          {bitrate != null && <Chip size="small" variant="outlined" label={`${bitrate}kbps`} />}
          {codec && <Chip size="small" variant="outlined" label={codec} />}
        </Stack>
        {entry.filePath && (
          <Box sx={{ mt: 0.5 }}>
            <PathLinks path={entry.filePath} aliases={pathAliases} vars={pathVars} />
          </Box>
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
export function RecommendationPanel({
  recommended,
  reason,
  evidence,
}: {
  recommended: string | undefined;
  reason: string | undefined;
  evidence: RecommendationEvidence | undefined;
}) {
  const spec = actionSpec(recommended);
  const undecidable = !spec || !spec.approvable;
  // No headline: this banner already renders `reason` above, with its own
  // fallback copy for holds that predate evidence-backed recommendations.
  const factsEvidence = regroupEvidence(evidence);
  const hasFacts = factsEvidence.facts.length > 0;

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
        <Typography variant="body2" sx={{ mb: hasFacts ? 1 : 0 }}>
          {reason}
        </Typography>
      )}
      {!reason && (
        <Typography
          variant="body2"
          sx={{
            color: 'text.secondary',
            mb: hasFacts ? 1 : 0,
          }}
        >
          This hold predates evidence-backed recommendations. Re-run the regroup scan to refresh it,
          or decide it here.
        </Typography>
      )}
      <EvidencePanel evidence={factsEvidence} />
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
export function ActionSelector({
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
export function ItemActions({
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
    <Stack
      direction="row"
      spacing={1.5}
      useFlexGap
      sx={[
        {
          alignItems: 'flex-start',
          flexWrap: 'wrap',
        },
        ...(Array.isArray(sx) ? sx : [sx]),
      ]}
    >
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
      <Stack
        direction="row"
        spacing={1}
        sx={[
          withSelector
            ? {
                mt: 0.5,
              }
            : {
                mt: 0,
              },
        ]}
      >
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
//
// `payload` is passed IN rather than parsed here. useRegroupLane's payloadFor
// carries a categorical rule -- "row renderers MUST use this rather than calling
// parsePayload themselves" -- and this component is rendered by a row renderer,
// so parsing again made the rule one with a silent exception. The cost was never
// the point (the useMemo and unmountOnExit meant one parse per expand, not per
// render); a MUST that quietly does not hold is a rule the next reader stops
// believing.
export function MemberFilesDetail({
  item,
  payload,
}: {
  item: ReviewItem;
  payload: ReviewPayload | null;
}) {
  const pathAliases = usePathAliases();
  const pathVars = usePathVars();
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
        <Stack
          direction="row"
          spacing={1}
          sx={{
            alignItems: 'center',
          }}
        >
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
              pathAliases={pathAliases}
              pathVars={pathVars}
            />
          ))}
        </Stack>
      )}
    </Box>
  );
}

// ---------------------------------------------------------------------------
// RegroupSpine -- buckets of holds, each expandable to its members.
// ---------------------------------------------------------------------------

function BucketHeader({ bucket, lane }: { bucket: RegroupBucket; lane: RegroupLane }) {
  const busy = lane.isKindBusy(bucket.kind);
  return (
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
        {/*
          Two numbers, on purpose. "Approve all" is kind-scoped on the server, so
          it acts on every pending hold of this kind -- not on the ones loaded
          here. When those differ, saying only the loaded count would understate
          what the button does by whatever the difference happens to be. On
          production today that is 484 shown against 714 acted on.

          🔴 `loadedForKind`, NEVER `items.length`. During the debounce window
          the local pass hides rows without unloading them, so feeding the
          visible count in here would make every keystroke look like the lane
          had failed to load rows it is holding -- and would understate the
          bulk buttons' scope at the same time. What the local pass hid is its
          own chip below, in its own words.

          🔴 AND THIS WHOLE CHIP IS SUPPRESSED WHILE A SEARCH IS ACTIVE. The
          term is pushed to the server now, so `loadedForKind` counts MATCHES
          while `totalForKind` still comes from the search-blind polled count.
          Comparing them under a search asks "did we fail to load rows that
          exist?" and answers "did the search narrow anything?", which is true
          of every useful search -- a three-hit search in a 714-hold kind would
          render a warning-coloured "3 of 714". The lane sets `truncated` false
          under a search for that reason; genuine truncation is still reported
          at panel grain, where both sides are search-scoped.
        */}
        {bucket.truncated ? (
          <Tooltip
            title={`${bucket.loadedForKind} loaded, ${bucket.totalForKind} pending on the server. Bulk actions below apply to all ${bucket.totalForKind}.`}
          >
            <Chip
              size="small"
              color="warning"
              variant="outlined"
              data-testid={`regroup-count-${bucket.kind}`}
              label={`${bucket.loadedForKind} of ${bucket.totalForKind}`}
            />
          </Tooltip>
        ) : (
          <Chip
            size="small"
            data-testid={`regroup-count-${bucket.kind}`}
            label={bucket.loadedForKind}
          />
        )}
        {bucket.hiddenBySearch > 0 && (
          <Tooltip
            title={`${bucket.hiddenBySearch} loaded hold${
              bucket.hiddenBySearch === 1 ? '' : 's'
            } of this kind do not match the search. They are hidden, not gone — bulk actions below still reach them.`}
          >
            <Chip
              size="small"
              variant="outlined"
              data-testid={`regroup-hidden-${bucket.kind}`}
              label={`${bucket.items.length} match the search`}
            />
          </Tooltip>
        )}
      </Box>
      <Stack direction="row" spacing={1}>
        <Tooltip title="Approves each hold with ITS OWN recommendation — there is no batch-wide action. Holds the classifier could not decide are skipped and listed, not approved.">
          <span>
            <Button
              size="small"
              variant="contained"
              color="success"
              startIcon={<CheckIcon />}
              disabled={busy}
              data-testid={`regroup-approve-all-${bucket.kind}`}
              onClick={() => lane.bulkAction(bucket.kind, 'approve')}
            >
              {`Approve all ${bucket.totalForKind}`}
            </Button>
          </span>
        </Tooltip>
        <Button
          size="small"
          variant="outlined"
          color="error"
          startIcon={<CloseIcon />}
          disabled={busy}
          data-testid={`regroup-reject-all-${bucket.kind}`}
          onClick={() => lane.bulkAction(bucket.kind, 'reject')}
        >
          {`Reject all ${bucket.totalForKind}`}
        </Button>
      </Stack>
    </Box>
  );
}

function SkipsAlert({ kind, lane }: { kind: string; lane: RegroupLane }) {
  const skipped = lane.skipsByKind[kind];
  if (!skipped || skipped.length === 0) return null;
  return (
    <Alert
      severity="warning"
      sx={{ mb: 1 }}
      data-testid={`regroup-skips-${kind}`}
      onClose={() => lane.dismissSkips(kind)}
    >
      <AlertTitle>
        {skipped.length} hold
        {skipped.length === 1 ? ' was' : 's were'} skipped — still pending
      </AlertTitle>
      <Typography variant="body2" sx={{ mb: 1 }}>
        Bulk approve uses each hold&apos;s own recommendation. These had none it would act on, so
        they were left alone. Open them below and choose an action.
      </Typography>
      <Stack spacing={0.5}>
        {skipped.map((s) => (
          <Typography key={s.id} variant="caption" sx={{ display: 'block' }}>
            <code>{s.id}</code>
            {s.action ? ` · ${labelForAction(s.action) || s.action}` : ''} — {s.reason}
          </Typography>
        ))}
      </Stack>
    </Alert>
  );
}

/** The four lane callbacks a row needs, hoisted so their identity is stable.
 *  Passing `lane` itself would defeat the memo: the lane object is rebuilt on
 *  every render. */
interface RegroupRowHandlers {
  onApprove: (item: ReviewItem) => void;
  onReject: (item: ReviewItem) => void;
  onActionChange: (id: string, value: string) => void;
}

/**
 * One regroup hold.
 *
 * Memoized because this lane renders up to REGROUP_FETCH_LIMIT (500) rows with
 * no page-size control -- the most rows of any lane -- and was the only one of
 * the three whose rows were inline JSX. Every row re-rendered whenever any row
 * became busy, whenever the search text changed, and on every poll tick.
 *
 * The props are deliberately all plain values or stable references: `payload`
 * comes from the lane's parse index so its identity survives a re-render, and
 * `handlers` is hoisted once. Passing `lane` directly, or an inline arrow for
 * any handler, leaves the memo present and inert -- see the same warning in
 * DupesPanel.
 */
const RegroupRow = memo(function RegroupRow({
  item,
  handlers,
  busy,
  payload,
  action,
}: {
  item: ReviewItem;
  handlers: RegroupRowHandlers;
  busy: boolean;
  payload: ReviewPayload | null;
  action: string;
}) {
  const recommended = payload?.recommendedAction;
  const recSpec = actionSpec(recommended);
  const needsHuman = !recSpec || !recSpec.approvable;
  return (
    <Accordion
      disableGutters
      data-testid={`regroup-row-${item.id}`}
      slotProps={{ transition: { unmountOnExit: true } }}
    >
      <AccordionSummary expandIcon={<ExpandMoreIcon />}>
        <Stack direction="row" spacing={1} sx={{ alignItems: 'center', flexGrow: 1, minWidth: 0 }}>
          <Typography variant="body2" noWrap sx={{ flexGrow: 1, minWidth: 0 }}>
            {item.summary || item.folder_ref || item.id}
          </Typography>
          {/* Shown COLLAPSED so a reviewer can triage a bucket without
                      opening every row. An undecidable hold is flagged rather
                      than hidden: those are the ones that need a person, and
                      they are the majority of the current queue. */}
          <Chip
            size="small"
            label={needsHuman ? 'Needs a decision' : `Rec: ${labelForAction(recommended)}`}
            color={needsHuman ? 'warning' : recSpec?.destructive ? 'default' : 'info'}
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
        <ItemActions
          item={item}
          action={action}
          busy={busy}
          withSelector
          onApprove={handlers.onApprove}
          onReject={handlers.onReject}
          onActionChange={handlers.onActionChange}
          sx={{ mb: 2 }}
        />
        <MemberFilesDetail item={item} payload={payload} />
        <ItemActions
          item={item}
          action={action}
          busy={busy}
          onApprove={handlers.onApprove}
          onReject={handlers.onReject}
          onActionChange={handlers.onActionChange}
          sx={{ mt: 2 }}
        />
      </AccordionDetails>
    </Accordion>
  );
});

export function RegroupSpine({ lane }: { lane: RegroupLane }) {
  // Hoisted once so every RegroupRow receives the same references. These three
  // are stable in the lane (approveItem/rejectItem read the chosen action
  // through a ref precisely so a change to one row's dropdown does not rebuild
  // them and re-render every other row).
  const handlers: RegroupRowHandlers = useMemo(
    () => ({
      onApprove: lane.approveItem,
      onReject: lane.rejectItem,
      onActionChange: lane.setAction,
    }),
    [lane.approveItem, lane.rejectItem, lane.setAction]
  );

  if (lane.loading && lane.buckets.length === 0) {
    return (
      <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }} data-testid="regroup-spine">
        <CircularProgress />
      </Box>
    );
  }

  if (lane.buckets.length === 0) {
    // 🔴 EMPTY AND FILTERED-EMPTY ARE DIFFERENT ANSWERS. This branch used to
    // say "Nothing to review 🎉" for both, which under a filter is a
    // congratulation on a queue that may hold hundreds of holds — the reviewer's
    // next step is "widen the filter", not "go home". The queue-empty copy now
    // comes from the lane descriptor, which has carried it unused since the
    // lane was ported.
    if (lane.filtersActive) {
      return (
        <Paper sx={{ p: 6, textAlign: 'center' }} data-testid="regroup-spine">
          <Typography variant="h6" gutterBottom data-testid="regroup-empty-filtered">
            No holds match these filters
          </Typography>
          <Typography variant="body2" sx={{ color: 'text.secondary', mb: 2 }}>
            {/* null = the count poll has not answered; the generic copy below
                is the honest fallback rather than an invented number. */}
            {lane.queueTotal !== null && lane.queueTotal > 0
              ? `The queue still holds ${lane.queueTotal} pending item${
                  lane.queueTotal === 1 ? '' : 's'
                }. Widen the filters to see them.`
              : 'Widen the filters to see the queue.'}
          </Typography>
          <Button size="small" variant="outlined" onClick={lane.clearFilters}>
            Clear filters
          </Button>
        </Paper>
      );
    }
    return (
      <Paper sx={{ p: 6, textAlign: 'center' }} data-testid="regroup-spine">
        <Typography variant="h6" gutterBottom data-testid="regroup-empty">
          Nothing to review 🎉
        </Typography>
        <Typography variant="body2" sx={{ color: 'text.secondary' }}>
          {regroupLane.emptyMessage}
        </Typography>
      </Paper>
    );
  }

  return (
    <Stack spacing={2} data-testid="regroup-spine" sx={{ p: 1 }}>
      {lane.buckets.map((bucket) => (
        <Paper key={bucket.kind} sx={{ p: 2 }}>
          <BucketHeader bucket={bucket} lane={lane} />
          <SkipsAlert kind={bucket.kind} lane={lane} />
          <Divider sx={{ mb: 1 }} />
          {bucket.items.map((item) => (
            // Resolved HERE, once per row, so each row receives plain values it
            // can be compared on rather than the whole churning lane object.
            <RegroupRow
              key={item.id}
              item={item}
              handlers={handlers}
              busy={lane.isItemBusy(item.id)}
              payload={lane.payloadFor(item)}
              action={lane.actionFor(item)}
            />
          ))}
        </Paper>
      ))}
    </Stack>
  );
}
