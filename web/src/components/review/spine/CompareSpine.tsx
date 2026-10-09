// file: web/src/components/review/spine/CompareSpine.tsx
// version: 1.14.0
// guid: 1e5b8d72-4c30-49a6-8f21-0b7e3a6c9d54
// last-edited: 2026-10-09
//
// The shared comparison spine: the surface that shows a reviewer what they are
// deciding between.
//
// All three renderers below are PORTED, not rewritten. They came out of
// MetadataReviewDialog (:739, :976, :1320) by mechanical substitution of the
// closure references they used to reach through -- `rowStates.get(id)` became
// `ctx.rowState(id)`, `handleApplyOne(id)` became a dispatched action, and so
// on -- with the JSX otherwise untouched. That was deliberate: the dialog is
// 2110 lines of accumulated decisions about what a reviewer needs to see, and
// retyping it from a reading is how ports lose the details nobody remembers
// were there. docs/port-inventory.md is the checklist; Phase 7 cannot delete
// the dialog until it passes.
//
// What the port CHANGES, and nothing else:
//
//   1. Actions are dispatched, not called. The renderers no longer touch the
//      API, toasts, or state setters; they emit a `MetadataAction` and the
//      owner decides what that means. This is what lets the same renderer serve
//      the workspace and be tested without a server.
//   2. `handleSkip` is split. The dialog's single handler toggled
//      'skipped' <-> 'pending' (:592), so the Skip button and the "Skipped"
//      chip called the same function and meant opposite things. Here they
//      dispatch `skip` and `unskip`. Net behaviour is identical; the intent is
//      now readable at the call site.
//   3. A third view mode. It was `auto` (the two-column card collapsing on
//      the spine's width); since 2026-10-07 it is `candidates` -- the full
//      ranked candidate list per book (./CandidatesCard.tsx), which keeps the
//      same container-query collapse.
//   4. `EvidenceSection` -- the recorded scoring derivation, which the dialog
//      never had. It is the reason the backend instrumentation exists.
//
// Not generic over lanes. These renderers are metadata-shaped throughout
// (`CandidateResult`, `duration_delta_sec`, provider chips), and a type
// parameter here would have been an abstraction over one real case and two
// guesses.
//
// The second real case has now arrived and the answer held: see
// ./DupesSpine.tsx. A CandidateResult is one book plus a proposal about it; a
// DedupCandidate is two books plus a claim about the pair, keyed on a number
// rather than a string. They share a page layout, not a row shape, so the dupes
// lane got a sibling rather than a generic, and `ReviewWorkspace` keeps the lane
// switch permanently rather than as a stopgap. Revisit only if regroup turns
// out to be genuinely the same shape as one of these two -- it is a third
// comparison (a proposed grouping change), so expect a third renderer.

import {
  Avatar,
  Box,
  Button,
  Checkbox,
  Chip,
  IconButton,
  LinearProgress,
  Stack,
  Tooltip,
  Typography,
} from '@mui/material';
import { memo, useMemo } from 'react';
import CloseIcon from '@mui/icons-material/Close';
import type {
  BulkApplyMode,
  CandidateResult,
  MetadataCandidate,
  PathAlias,
} from '../../../services/api';
import type { MetadataAction } from '../reviewActions';
import { EvidencePanel } from '../evidence/EvidencePanel';
import { metadataEvidence, type CandidateDetailState } from '../evidence/adapters';
import { usePathAliases } from '../../common/PathLinks';
import { usePathVars, type PathVar } from '../../../utils/formatPath';
import {
  SOURCE_COLORS,
  SPINE_TWO_COLUMN_MIN,
  formatDuration,
  getRowSx,
  isRowActionable,
  noCandidateLabel,
  runtimeDiffers,
  type RowState,
} from './rowState';
import { BookInfoPanel } from './BookInfoPanel';
import { bookSummaryLine } from './bookInfo';
import type { SpineViewMode } from './viewMode';
import { CandidatesCard, type CandidatesContext } from './CandidatesCard';

/**
 * "Why did it score that?" -- the recorded derivation for one candidate.
 *
 * This is the one thing in the spine that the dialog never had. The metadata
 * scorer now ships `score_breakdown` alongside the score, and `metadataEvidence`
 * turns it into a waterfall: base, then each multiplier and term in the order the
 * pipeline applied them, replaying to the number on the chip.
 *
 * A waterfall rather than the dedup lane's stacked share bar, because metadata
 * scoring is `(base x factors) + terms` and a multiplicative factor has no share
 * of a total. Feeding it to the share bar would produce segments summing to
 * nothing meaningful -- worse than no bar, because it would still look complete.
 *
 * If the steps do not replay to the shipped score, the panel says so rather than
 * rendering a confident-looking breakdown of a number it cannot account for.
 */
/**
 * The candidate's subtitle, when it adds something the title does not already
 * say. Some providers (Google Books) fold the subtitle into the title
 * ("Star Wars: A New Dawn"), so repeating it underneath would be noise; others
 * (Audible) keep them apart and the subtitle is the only place it shows.
 */
function candidateSubtitle(candidate: MetadataCandidate): string | null {
  const subtitle = candidate.subtitle?.trim();
  if (!subtitle) return null;
  if (candidate.title.toLowerCase().includes(subtitle.toLowerCase())) return null;
  return subtitle;
}

function EvidenceSection({
  candidate,
  detail,
}: {
  candidate: MetadataCandidate;
  /**
   * Whether `candidate` is the full row yet. The lane shows the index row
   * (no breakdown) first and swaps the full row in when its detail fetch
   * lands, so the panel must be told which it has: an index row with no
   * breakdown is "still loading", not "scored without a derivation".
   */
  detail: CandidateDetailState;
}) {
  return (
    <Box sx={{ mt: 2 }} data-testid="evidence-section">
      <Typography variant="subtitle2" gutterBottom>
        How this score was reached
      </Typography>
      <EvidencePanel evidence={metadataEvidence(candidate, detail)} />
    </Box>
  );
}

/** A set of books that all matched the same candidate -- the ambiguity unit. */
export interface CandidateGroup {
  key: string;
  candidate: MetadataCandidate;
  results: CandidateResult[];
}

/**
 * Everything the renderers used to reach out of their closure for.
 *
 * Deliberately accessor-shaped (`rowState(id)`) rather than data-shaped
 * (`rowStates: Map`). The owner may hold that state in a Map, a reducer, or a
 * server cache, and the spine should not be rewritten when it changes.
 *
 * `expandedId` / `onToggleExpand` live here rather than in a shared context
 * because expansion is COMPACT-ONLY: the dialog referenced `expandedId` at
 * exactly one site (:978), inside the compact row. Two-column and grouped cards
 * show everything already and have nothing to expand.
 */
export interface SpineContext {
  rowState: (id: string) => RowState | undefined;
  isSelected: (id: string) => boolean;
  onToggleSelect: (id: string) => void;
  onPreviewCover: (url: string) => void;
  onAction: (action: MetadataAction) => void;
  /** Compact mode only. Single-open: opening one closes the other. */
  expandedId: string | null;
  onToggleExpand: (id: string) => void;
  /**
   * Whether this book's candidate is its full row yet -- see
   * CandidateDetailState. The lane answers from its per-page detail fetch;
   * an owner whose rows are always full answers 'loaded'.
   */
  detailState: (id: string) => CandidateDetailState;
  /**
   * The lane's bulk-apply toggle, so a group's Apply All can say when it will
   * replace existing values. Absent reads as 'fill'.
   */
  bulkApplyMode?: BulkApplyMode;
}

/**
 * The CALLBACK half of SpineContext, split out so the row renderers can be
 * memoized.
 *
 * `SpineContext` is rebuilt by useMetadataLane whenever `rowStates`,
 * `selectedIds` or `expandedId` changes -- which is to say on every checkbox
 * tick and every expand. Passing the whole context to each row therefore gave
 * every row a new prop on every selection change, so all of them re-rendered to
 * repaint one. At a page size of 100 that is 99 wasted row renders per click.
 *
 * The callbacks themselves are stable (`toggleSelect` and `toggleExpand` are
 * `useCallback(..., [])`; `dispatch` depends only on things that do not move
 * when a checkbox is ticked), so lifting them into their own object gives the
 * memoized rows a prop that holds still.
 */
export type SpineHandlers = Pick<
  SpineContext,
  'onToggleSelect' | 'onPreviewCover' | 'onAction' | 'onToggleExpand'
>;

/**
 * What one row renderer needs. The three per-row VALUES are resolved by the
 * spine and passed as plain props rather than looked up through the context,
 * so a row's props change only when that row's own state does.
 */
export interface SpineRowProps {
  r: CandidateResult;
  selected: boolean;
  rowState: RowState | undefined;
  /** Compact mode only; ignored by the two-column renderers. */
  expanded: boolean;
  /** Resolved by the spine from `ctx.detailState`, like `rowState`. */
  detail: CandidateDetailState;
  handlers: SpineHandlers;
  pathAliases: PathAlias[];
  /** Threaded, not re-derived per row -- see the vars prop on PathLinksProps. */
  pathVars: PathVar[];
}

export type { SpineViewMode } from './viewMode';

function GroupedCard({
  group,
  ctx,
  pathAliases,
  pathVars,
}: {
  group: CandidateGroup;
  ctx: SpineContext;
  pathAliases: PathAlias[];
  pathVars: PathVar[];
}) {
  const c = group.candidate;
  const actionableIds = group.results
    .filter((r) => isRowActionable(ctx.rowState(r.book.id)))
    .map((r) => r.book.id);
  const allApplied = group.results.every((r) => ctx.rowState(r.book.id) === 'applied');
  const allRejected = group.results.every((r) => ctx.rowState(r.book.id) === 'rejected');

  return (
    <Box
      key={group.key}
      sx={{ p: 2, mb: 1, border: 2, borderColor: 'primary.dark', borderRadius: 1 }}
    >
      <Typography
        variant="caption"
        color="primary"
        sx={{ fontWeight: 700, mb: 1, display: 'block' }}
      >
        {group.results.length} files matched to the same book
      </Typography>
      <Stack direction="row" spacing={2}>
        {/* Left: stacked book rows, each with an X to split from group */}
        <Box sx={{ flex: 1 }}>
          <Stack spacing={1.5}>
            {group.results.map((r) => (
              <Stack
                key={r.book.id}
                direction="row"
                spacing={1}
                sx={{
                  alignItems: 'flex-start',
                }}
              >
                <Tooltip title="Separate from group">
                  <IconButton
                    size="small"
                    onClick={() =>
                      ctx.onAction({ lane: 'metadata', type: 'ungroup', id: r.book.id })
                    }
                  >
                    <CloseIcon fontSize="small" />
                  </IconButton>
                </Tooltip>
                <Avatar
                  src={r.book.cover_url || ''}
                  variant="rounded"
                  sx={{ width: 40, height: 50 }}
                />
                <Box sx={{ minWidth: 0 }}>
                  <BookInfoPanel book={r.book} pathAliases={pathAliases} pathVars={pathVars} />
                  {ctx.rowState(r.book.id) === 'applied' && (
                    <Chip label="Applied" size="small" color="success" sx={{ mt: 0.5 }} />
                  )}
                  {ctx.rowState(r.book.id) === 'rejected' && (
                    <Chip label="Rejected" size="small" color="error" sx={{ mt: 0.5 }} />
                  )}
                  {ctx.rowState(r.book.id) === 'skipped' && (
                    <Chip label="Skipped" size="small" sx={{ mt: 0.5 }} />
                  )}
                </Box>
              </Stack>
            ))}
          </Stack>
        </Box>

        {/* Right: shared candidate */}
        <Box sx={{ flex: 1 }}>
          <Stack
            direction="row"
            spacing={1}
            sx={{
              alignItems: 'flex-start',
            }}
          >
            <Avatar
              src={c.cover_url || ''}
              variant="rounded"
              sx={{ width: 60, height: 80, cursor: c.cover_url ? 'pointer' : 'default' }}
              onClick={() => c.cover_url && ctx.onPreviewCover(c.cover_url)}
            />
            <Box sx={{ minWidth: 0, flex: 1 }}>
              <Typography
                variant="body2"
                sx={{
                  fontWeight: 'bold',
                }}
              >
                {c.title}
              </Typography>
              <Typography variant="body2">{c.author}</Typography>
              {c.narrator && (
                <Typography
                  variant="body2"
                  sx={{
                    color: 'text.secondary',
                  }}
                >
                  Narrated by {c.narrator}
                </Typography>
              )}
              {c.series && (
                <Typography variant="body2">
                  Series: {c.series}
                  {c.series_position ? ` · Book ${c.series_position}` : ''}
                </Typography>
              )}
              {c.year && (
                <Typography
                  variant="caption"
                  sx={{
                    display: 'block',
                  }}
                >
                  {c.year}
                </Typography>
              )}
              {c.publisher && (
                <Typography
                  variant="caption"
                  sx={{
                    display: 'block',
                  }}
                >
                  {c.publisher}
                </Typography>
              )}
              <Stack direction="row" spacing={0.5} sx={{ mt: 0.5 }}>
                <Chip
                  label={`${Math.round(c.score * 100)}`}
                  size="small"
                  color={c.score >= 0.85 ? 'success' : c.score >= 0.6 ? 'warning' : 'default'}
                />
                <Chip
                  label={c.source}
                  size="small"
                  color={SOURCE_COLORS[c.source] || 'default'}
                  variant="outlined"
                />
              </Stack>
              {!allApplied && !allRejected && actionableIds.length > 0 && (
                <Stack direction="row" spacing={1} sx={{ mt: 1 }}>
                  <Button
                    size="small"
                    variant="contained"
                    color={ctx.bulkApplyMode === 'replace' ? 'warning' : 'success'}
                    data-testid="group-apply-all"
                    onClick={() =>
                      ctx.onAction({ lane: 'metadata', type: 'applySelected', ids: actionableIds })
                    }
                  >
                    {`${ctx.bulkApplyMode === 'replace' ? 'Apply All, replace existing' : 'Apply All'} (${actionableIds.length})`}
                  </Button>
                  <Button
                    size="small"
                    variant="outlined"
                    color="error"
                    onClick={() =>
                      ctx.onAction({ lane: 'metadata', type: 'rejectGroup', ids: actionableIds })
                    }
                  >
                    Reject All
                  </Button>
                  <Button
                    size="small"
                    variant="text"
                    onClick={() =>
                      group.results.forEach((r) =>
                        ctx.onAction({ lane: 'metadata', type: 'skip', id: r.book.id })
                      )
                    }
                  >
                    Skip All
                  </Button>
                </Stack>
              )}
              {allApplied && (
                <Chip label="All Applied" size="small" color="success" sx={{ mt: 1 }} />
              )}
              {allRejected && (
                <Chip label="All Rejected" size="small" color="error" sx={{ mt: 1 }} />
              )}
            </Box>
          </Stack>
        </Box>
      </Stack>
    </Box>
  );
}

const CompactRow = memo(function CompactRow({
  r,
  selected,
  rowState,
  expanded,
  detail,
  handlers,
  pathAliases,
  pathVars,
}: SpineRowProps) {
  const bookId = r.book.id;
  const isExpanded = expanded;

  return (
    <Box key={bookId}>
      <Stack
        direction="row"
        spacing={1}
        onClick={() => handlers.onToggleExpand(bookId)}
        sx={{
          alignItems: 'center',
          p: 1,
          cursor: 'pointer',
          '&:hover': { bgcolor: 'action.hover' },
          ...getRowSx(rowState),
        }}
      >
        <Checkbox
          size="small"
          checked={selected}
          onClick={(e) => e.stopPropagation()}
          onChange={() => handlers.onToggleSelect(bookId)}
          disabled={!isRowActionable(rowState)}
        />
        <Avatar
          src={r.candidate?.cover_url || r.book.cover_url || ''}
          variant="rounded"
          sx={{ width: 40, height: 50, cursor: 'pointer' }}
          onClick={(e) => {
            e.stopPropagation();
            handlers.onPreviewCover(r.candidate?.cover_url || r.book.cover_url || '');
          }}
        />
        <Box sx={{ flex: 1, minWidth: 0 }}>
          {/* `component="span"`, not the default `<p>`: the no-match/error
              branches below render a Chip, which is a <div>, and a <div> inside
              a <p> is invalid HTML -- the browser closes the paragraph early and
              the chip escapes the row's layout. Carried in from the dialog by
              the mechanical port; the surrounding Box is the block container, so
              nothing needs the <p>. */}
          <Typography variant="body2" component="span" noWrap sx={{ display: 'block' }}>
            {r.book.title}
            {r.candidate ? (
              <>
                {' \u2192 '}
                <strong>{r.candidate.title}</strong>
              </>
            ) : (
              <Chip
                label={noCandidateLabel(r).label}
                title={noCandidateLabel(r).detail}
                color={noCandidateLabel(r).color}
                size="small"
                sx={{ ml: 1 }}
                data-testid="no-candidate-chip"
              />
            )}
          </Typography>
          <Typography
            variant="caption"
            noWrap
            sx={{ display: 'block', color: 'text.secondary' }}
            data-testid="book-summary-line"
          >
            {bookSummaryLine(r.book)}
          </Typography>
        </Box>
        {r.candidate && (
          <>
            <Chip
              label={`${Math.round(r.candidate.score * 100)}`}
              size="small"
              color={
                r.candidate.score >= 0.85
                  ? 'success'
                  : r.candidate.score >= 0.6
                    ? 'warning'
                    : 'default'
              }
            />
            <Chip
              label={r.candidate.source}
              size="small"
              color={SOURCE_COLORS[r.candidate.source] || 'default'}
              variant="outlined"
            />
            {(r.candidate.audible_rating_overall ?? 0) > 0 && (
              <Chip
                label={`★ ${r.candidate.audible_rating_overall!.toFixed(1)}${(r.candidate.audible_rating_count ?? 0) > 0 ? ` (${r.candidate.audible_rating_count!.toLocaleString()})` : ''}`}
                size="small"
                variant="outlined"
                sx={{ fontWeight: 500 }}
              />
            )}
            {(r.candidate.google_rating_average ?? 0) > 0 && (
              <Chip
                label={`G★ ${r.candidate.google_rating_average!.toFixed(1)}${(r.candidate.google_rating_count ?? 0) > 0 ? ` (${r.candidate.google_rating_count!.toLocaleString()})` : ''}`}
                size="small"
                variant="outlined"
                sx={{ fontWeight: 500 }}
              />
            )}
            {runtimeDiffers(r.candidate?.duration_delta_sec) && (
              <Chip
                label={`⚠ runtime differs by ${formatDuration(Math.abs(r.candidate.duration_delta_sec!))}`}
                color="warning"
                size="small"
                sx={{ fontWeight: 500 }}
              />
            )}
          </>
        )}
        {isRowActionable(rowState) && r.candidate && (
          <>
            <Button
              size="small"
              variant="contained"
              color="success"
              onClick={(e) => {
                e.stopPropagation();
                handlers.onAction({ lane: 'metadata', type: 'apply', id: bookId });
              }}
            >
              Apply
            </Button>
            <Button
              size="small"
              variant="outlined"
              color="error"
              onClick={(e) => {
                e.stopPropagation();
                handlers.onAction({ lane: 'metadata', type: 'reject', id: bookId });
              }}
            >
              Reject
            </Button>
            <Button
              size="small"
              variant="text"
              onClick={(e) => {
                e.stopPropagation();
                handlers.onAction({ lane: 'metadata', type: 'skip', id: bookId });
              }}
            >
              Skip
            </Button>
          </>
        )}
        {rowState === 'skipped' && (
          <Chip
            label="Skipped"
            size="small"
            onClick={(e) => {
              e.stopPropagation();
              handlers.onAction({ lane: 'metadata', type: 'unskip', id: bookId });
            }}
            sx={{ cursor: 'pointer' }}
          />
        )}
        {rowState === 'applied' && <Chip label="Applied" size="small" color="success" />}
        {rowState === 'rejected' && (
          <Chip
            label="Rejected — click to undo"
            size="small"
            color="error"
            onClick={(e) => {
              e.stopPropagation();
              handlers.onAction({ lane: 'metadata', type: 'unreject', id: bookId });
            }}
            sx={{ cursor: 'pointer' }}
          />
        )}
      </Stack>

      {/* Expanded two-column detail for this row. The book half shows for
          every row, candidate or not: it is the book's full info. */}
      {isExpanded && (
        <Box sx={{ p: 2, pl: 7, bgcolor: 'action.hover', borderRadius: 1 }}>
          <Stack direction="row" spacing={2}>
            <Box sx={{ flex: 1, minWidth: 0 }}>
              <Typography variant="subtitle2" gutterBottom>
                Current
              </Typography>
              <Stack direction="row" spacing={1} sx={{ alignItems: 'flex-start' }}>
                <Avatar
                  src={r.book.cover_url || ''}
                  variant="rounded"
                  sx={{ width: 60, height: 80, cursor: r.book.cover_url ? 'pointer' : 'default' }}
                  onClick={() => r.book.cover_url && handlers.onPreviewCover(r.book.cover_url)}
                />
                <BookInfoPanel book={r.book} pathAliases={pathAliases} pathVars={pathVars} />
              </Stack>
            </Box>
            {r.candidate && (
              <Box sx={{ flex: 1 }}>
                <Typography variant="subtitle2" gutterBottom>
                  Proposed
                </Typography>
                <Stack
                  direction="row"
                  spacing={1}
                  sx={{
                    alignItems: 'flex-start',
                  }}
                >
                  <Avatar
                    src={r.candidate.cover_url || ''}
                    variant="rounded"
                    sx={{
                      width: 60,
                      height: 80,
                      cursor: r.candidate?.cover_url ? 'pointer' : 'default',
                    }}
                    onClick={() =>
                      r.candidate?.cover_url && handlers.onPreviewCover(r.candidate.cover_url)
                    }
                  />
                  <Box>
                    <Typography
                      variant="body2"
                      sx={{
                        fontWeight: 'bold',
                      }}
                    >
                      {r.candidate.title}
                    </Typography>
                    {candidateSubtitle(r.candidate) && (
                      <Typography variant="body2" sx={{ fontStyle: 'italic' }}>
                        {candidateSubtitle(r.candidate)}
                      </Typography>
                    )}
                    <Typography variant="body2">{r.candidate.author}</Typography>
                    {r.candidate.narrator && (
                      <Typography
                        variant="body2"
                        sx={{
                          color: 'text.secondary',
                        }}
                      >
                        Narrated by {r.candidate.narrator}
                      </Typography>
                    )}
                    {r.candidate.series && (
                      <Typography variant="body2">
                        Series: {r.candidate.series}
                        {r.candidate.series_position
                          ? ` \u00b7 Book ${r.candidate.series_position}`
                          : ''}
                      </Typography>
                    )}
                    {r.candidate.year && (
                      <Typography
                        variant="caption"
                        sx={{
                          display: 'block',
                        }}
                      >
                        {r.candidate.year}
                      </Typography>
                    )}
                    {r.candidate.publisher && (
                      <Typography
                        variant="caption"
                        sx={{
                          display: 'block',
                        }}
                      >
                        {r.candidate.publisher}
                      </Typography>
                    )}
                    <Chip
                      label={`${Math.round(r.candidate.score * 100)}`}
                      size="small"
                      color={
                        r.candidate.score >= 0.85
                          ? 'success'
                          : r.candidate.score >= 0.6
                            ? 'warning'
                            : 'default'
                      }
                      sx={{ mt: 0.5, mr: 0.5 }}
                    />
                    <Chip
                      label={r.candidate.source}
                      size="small"
                      color={SOURCE_COLORS[r.candidate.source] || 'default'}
                      variant="outlined"
                      sx={{ mt: 0.5, mr: 0.5 }}
                    />
                    {(r.candidate.audible_rating_overall ?? 0) > 0 && (
                      <Chip
                        label={`★ ${r.candidate.audible_rating_overall!.toFixed(1)}${(r.candidate.audible_rating_count ?? 0) > 0 ? ` (${r.candidate.audible_rating_count!.toLocaleString()})` : ''}`}
                        size="small"
                        variant="outlined"
                        sx={{ mt: 0.5, mr: 0.5, fontWeight: 500 }}
                      />
                    )}
                    {(r.candidate.google_rating_average ?? 0) > 0 && (
                      <Chip
                        label={`G★ ${r.candidate.google_rating_average!.toFixed(1)}${(r.candidate.google_rating_count ?? 0) > 0 ? ` (${r.candidate.google_rating_count!.toLocaleString()})` : ''}`}
                        size="small"
                        variant="outlined"
                        sx={{ mt: 0.5, fontWeight: 500 }}
                      />
                    )}
                  </Box>
                </Stack>
              </Box>
            )}
          </Stack>
          {r.candidate && <EvidenceSection candidate={r.candidate} detail={detail} />}
        </Box>
      )}
    </Box>
  );
});

const TwoColumnCard = memo(function TwoColumnCard({
  r,
  selected,
  rowState,
  detail,
  handlers,
  pathAliases,
  pathVars,
}: SpineRowProps) {
  const bookId = r.book.id;

  return (
    <Box
      key={bookId}
      sx={{
        p: 2,
        mb: 1,
        border: 1,
        borderColor: 'divider',
        ...getRowSx(rowState),
      }}
    >
      <Stack direction="row" spacing={2}>
        {/* Left: current book info */}
        <Box sx={{ flex: 1 }}>
          <Stack
            direction="row"
            spacing={1}
            sx={{
              alignItems: 'flex-start',
            }}
          >
            <Checkbox
              size="small"
              checked={selected}
              onChange={() => handlers.onToggleSelect(bookId)}
              disabled={!isRowActionable(rowState)}
            />
            <Avatar
              src={r.book.cover_url || ''}
              variant="rounded"
              sx={{ width: 60, height: 80, cursor: r.book.cover_url ? 'pointer' : 'default' }}
              onClick={() => r.book.cover_url && handlers.onPreviewCover(r.book.cover_url)}
            />
            <BookInfoPanel book={r.book} pathAliases={pathAliases} pathVars={pathVars} />
          </Stack>
        </Box>

        {/* Right: proposed match */}
        <Box sx={{ flex: 1 }}>
          {r.candidate ? (
            <Stack
              direction="row"
              spacing={1}
              sx={{
                alignItems: 'flex-start',
              }}
            >
              <Avatar
                src={r.candidate.cover_url || ''}
                variant="rounded"
                sx={{
                  width: 60,
                  height: 80,
                  cursor: r.candidate?.cover_url ? 'pointer' : 'default',
                }}
                onClick={() =>
                  r.candidate?.cover_url && handlers.onPreviewCover(r.candidate.cover_url)
                }
              />
              <Box sx={{ minWidth: 0, flex: 1 }}>
                <Typography
                  variant="body2"
                  sx={{
                    fontWeight: 'bold',
                  }}
                >
                  {r.candidate.title}
                </Typography>
                {candidateSubtitle(r.candidate) && (
                  <Typography variant="body2" sx={{ fontStyle: 'italic' }}>
                    {candidateSubtitle(r.candidate)}
                  </Typography>
                )}
                <Typography variant="body2">{r.candidate.author}</Typography>
                {r.candidate.narrator && (
                  <Typography
                    variant="body2"
                    sx={{
                      color: 'text.secondary',
                    }}
                  >
                    Narrated by {r.candidate.narrator}
                  </Typography>
                )}
                {r.candidate.series && (
                  <Typography variant="body2">
                    Series: {r.candidate.series}
                    {r.candidate.series_position
                      ? ` \u00b7 Book ${r.candidate.series_position}`
                      : ''}
                  </Typography>
                )}
                {r.candidate.year && (
                  <Typography
                    variant="caption"
                    sx={{
                      display: 'block',
                    }}
                  >
                    {r.candidate.year}
                  </Typography>
                )}
                {r.candidate.publisher && (
                  <Typography
                    variant="caption"
                    sx={{
                      display: 'block',
                    }}
                  >
                    {r.candidate.publisher}
                  </Typography>
                )}
                {(r.candidate.duration_sec ?? 0) > 0 && (
                  <Typography
                    variant="caption"
                    sx={{
                      display: 'block',
                    }}
                  >
                    Duration: {formatDuration(r.candidate.duration_sec!)}
                  </Typography>
                )}
                <Stack
                  direction="row"
                  spacing={0.5}
                  sx={{
                    flexWrap: 'wrap',
                    mt: 0.5,
                  }}
                >
                  <Chip
                    label={`${Math.round(r.candidate.score * 100)}`}
                    size="small"
                    color={
                      r.candidate.score >= 0.85
                        ? 'success'
                        : r.candidate.score >= 0.6
                          ? 'warning'
                          : 'default'
                    }
                  />
                  <Chip
                    label={r.candidate.source}
                    size="small"
                    color={SOURCE_COLORS[r.candidate.source] || 'default'}
                    variant="outlined"
                  />
                  {(r.candidate.audible_rating_overall ?? 0) > 0 && (
                    <Chip
                      label={`★ ${r.candidate.audible_rating_overall!.toFixed(1)}${(r.candidate.audible_rating_count ?? 0) > 0 ? ` (${r.candidate.audible_rating_count!.toLocaleString()})` : ''}`}
                      size="small"
                      variant="outlined"
                      sx={{ fontWeight: 500 }}
                    />
                  )}
                  {(r.candidate.google_rating_average ?? 0) > 0 && (
                    <Chip
                      label={`G★ ${r.candidate.google_rating_average!.toFixed(1)}${(r.candidate.google_rating_count ?? 0) > 0 ? ` (${r.candidate.google_rating_count!.toLocaleString()})` : ''}`}
                      size="small"
                      variant="outlined"
                      sx={{ fontWeight: 500 }}
                    />
                  )}
                </Stack>
                {isRowActionable(rowState) && (
                  <Stack direction="row" spacing={1} sx={{ mt: 1 }}>
                    <Button
                      size="small"
                      variant="contained"
                      color="success"
                      onClick={() =>
                        handlers.onAction({ lane: 'metadata', type: 'apply', id: bookId })
                      }
                    >
                      Apply
                    </Button>
                    <Button
                      size="small"
                      variant="outlined"
                      color="error"
                      onClick={() =>
                        handlers.onAction({ lane: 'metadata', type: 'reject', id: bookId })
                      }
                    >
                      Reject
                    </Button>
                    <Button
                      size="small"
                      variant="text"
                      onClick={() =>
                        handlers.onAction({ lane: 'metadata', type: 'skip', id: bookId })
                      }
                    >
                      Skip
                    </Button>
                  </Stack>
                )}
                {rowState === 'skipped' && (
                  <Chip
                    label="Skipped — click to undo"
                    size="small"
                    onClick={() =>
                      handlers.onAction({ lane: 'metadata', type: 'unskip', id: bookId })
                    }
                    sx={{ cursor: 'pointer', mt: 1 }}
                  />
                )}
                {rowState === 'rejected' && (
                  <Chip
                    label="Rejected — click to undo"
                    size="small"
                    color="error"
                    onClick={() =>
                      handlers.onAction({ lane: 'metadata', type: 'unreject', id: bookId })
                    }
                    sx={{ cursor: 'pointer', mt: 1 }}
                  />
                )}
                {rowState === 'applied' && (
                  <Chip label="Applied" size="small" color="success" sx={{ mt: 1 }} />
                )}
              </Box>
            </Stack>
          ) : (
            <Box sx={{ display: 'flex', alignItems: 'center', height: '100%' }}>
              <Chip
                label={noCandidateLabel(r).label}
                title={noCandidateLabel(r).detail}
                color={noCandidateLabel(r).color}
                data-testid="no-candidate-chip"
              />
            </Box>
          )}
        </Box>
      </Stack>
      {r.candidate && <EvidenceSection candidate={r.candidate} detail={detail} />}
    </Box>
  );
});

/** Re-exported from ./rowState, where the candidates card reads it too. */
export { SPINE_TWO_COLUMN_MIN };

/**
 * The comparison surface.
 *
 * Grouped results always render as grouped cards regardless of view mode: a
 * group is several books competing for ONE candidate, and there is no faithful way
 * to show that as a row per book -- each row would repeat the same candidate and
 * imply each book had its own match.
 */
export function CompareSpine({
  rows,
  groups = [],
  viewMode,
  ctx,
  emptyMessage = 'Nothing to compare.',
  loading = false,
  errored = false,
  candidates,
}: {
  rows: CandidateResult[];
  groups?: CandidateGroup[];
  viewMode: SpineViewMode;
  ctx: SpineContext;
  emptyMessage?: string;
  /**
   * A load is in flight. Without this the spine cannot tell "still fetching"
   * from "fetched, and there is nothing" -- and it renders the SAME empty copy
   * for both, so a slow server looks like an empty queue.
   */
  loading?: boolean;
  /**
   * The load failed. The panel owns the error message, so the spine's job here
   * is only to STOP TALKING: `emptyMessage` tells the reviewer to go search
   * providers, which is advice that cannot help and is not even true when the
   * request 500'd -- there may be thousands of rows behind a server that is
   * down. Rendering the Alert above an unchanged "nothing to review" is the
   * exact symptom this change exists to remove.
   */
  errored?: boolean;
  /**
   * The candidates view's loader and apply. Without it the `candidates` mode
   * falls back to the two-column card (a spine with no search wiring).
   */
  candidates?: CandidatesContext;
}) {
  // Called once here and threaded down to every render site as a plain prop --
  // the renderers (GroupedCard, CompactRow, TwoColumnCard, CandidatesCard) stay
  // pure and don't each re-fetch config on their own.
  const pathAliases = usePathAliases();
  const pathVars = usePathVars();

  // The stable half of `ctx`. Keyed on the individual callbacks, NOT on `ctx`
  // itself: `ctx` gets a new identity on every selection and expand change,
  // while the callbacks inside it do not move. This is what lets the memoized
  // rows below actually skip.
  //
  // This MUST stay ABOVE the early returns below. Those returns are
  // conditional, so a hook placed after them is skipped on a loading or errored
  // render and called on a populated one -- React counts hooks by call order
  // and throws "Rendered more hooks than during the previous render" on the
  // transition back. tsc cannot see this; only the order protects it.
  const handlers: SpineHandlers = useMemo(
    () => ({
      onToggleSelect: ctx.onToggleSelect,
      onPreviewCover: ctx.onPreviewCover,
      onAction: ctx.onAction,
      onToggleExpand: ctx.onToggleExpand,
    }),
    [ctx.onToggleSelect, ctx.onPreviewCover, ctx.onAction, ctx.onToggleExpand]
  );

  // Both of these run BEFORE the empty branch. Order is the whole fix: the
  // empty message is a claim about the server's answer, so it may only be made
  // once there IS an answer and it succeeded.
  if (rows.length === 0 && groups.length === 0 && loading) {
    return (
      <Box sx={{ p: 3 }} data-testid="spine-loading">
        <LinearProgress />
        <Typography variant="body2" sx={{ color: 'text.secondary', fontStyle: 'italic', mt: 2 }}>
          Loading the review queue…
        </Typography>
      </Box>
    );
  }

  if (rows.length === 0 && groups.length === 0 && errored) {
    // Deliberately renders nothing. The panel has already said what went wrong
    // and offered Retry; a second message here would either repeat it or, worse,
    // contradict it with advice that assumes the load worked.
    return null;
  }

  if (rows.length === 0 && groups.length === 0) {
    return (
      <Box sx={{ p: 3 }} data-testid="spine-empty">
        <Typography variant="body2" sx={{ color: 'text.secondary', fontStyle: 'italic' }}>
          {emptyMessage}
        </Typography>
      </Box>
    );
  }

  return (
    <Box
      data-testid="compare-spine"
      data-view-mode={viewMode}
      sx={{
        // The spine IS the container the candidates card's query resolves against. This
        // declaration has to be on this element: put it on the row and
        // `@container` measures the row, which is already as wide as the spine,
        // and the collapse never fires.
        containerType: 'inline-size',
        containerName: 'spine',
      }}
    >
      {groups.map((group) => (
        <GroupedCard
          key={group.key}
          group={group}
          ctx={ctx}
          pathAliases={pathAliases}
          pathVars={pathVars}
        />
      ))}

      {rows.map((r) => {
        // Resolved HERE, once per row, so each row receives plain values it can
        // be compared on rather than the whole churning context.
        const rowProps = {
          r,
          selected: ctx.isSelected(r.book.id),
          rowState: ctx.rowState(r.book.id),
          expanded: ctx.expandedId === r.book.id,
          detail: ctx.detailState(r.book.id),
          handlers,
          pathAliases,
          pathVars,
        };
        return viewMode === 'compact' ? (
          <CompactRow key={r.book.id} {...rowProps} />
        ) : viewMode === 'two-column' ? (
          <TwoColumnCard key={r.book.id} {...rowProps} />
        ) : candidates ? (
          <CandidatesCard key={r.book.id} {...rowProps} cands={candidates} />
        ) : (
          <TwoColumnCard key={r.book.id} {...rowProps} />
        );
      })}
    </Box>
  );
}
