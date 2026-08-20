// file: web/src/components/review/spine/CompareSpine.tsx
// version: 1.1.0
// guid: 1e5b8d72-4c30-49a6-8f21-0b7e3a6c9d54
// last-edited: 2026-08-20
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
//   3. `auto` view mode -- see below. The one addition PLAN.md authorises.
//   4. `EvidenceSection` -- the recorded scoring derivation, which the dialog
//      never had. It is the reason the backend instrumentation exists.
//
// Not generic over lanes. These renderers are metadata-shaped throughout
// (`CandidateResult`, `duration_delta_sec`, provider chips), and a type
// parameter here would be an abstraction over one real case and two guesses.
// The dupes and regroup lanes generalise it when they arrive; `ReviewWorkspace`
// holds the lane switch in the meantime.

import {
  Avatar,
  Box,
  Button,
  Checkbox,
  Chip,
  IconButton,
  Stack,
  Tooltip,
  Typography,
} from '@mui/material';
import CloseIcon from '@mui/icons-material/Close';
import type { CandidateResult, MetadataCandidate } from '../../../services/api';
import type { MetadataAction } from '../reviewActions';
import { EvidencePanel } from '../evidence/EvidencePanel';
import { metadataEvidence } from '../evidence/adapters';
import {
  SOURCE_COLORS,
  formatDuration,
  formatFileSize,
  getRowSx,
  isRowActionable,
  type RowState,
} from './rowState';

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
function EvidenceSection({ candidate }: { candidate: MetadataCandidate }) {
  return (
    <Box sx={{ mt: 2 }} data-testid="evidence-section">
      <Typography variant="subtitle2" gutterBottom>
        How this score was reached
      </Typography>
      <EvidencePanel evidence={metadataEvidence(candidate)} />
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
}

/**
 * `compact` and `two-column` are the reviewer's explicit choice, carried over
 * from the dialog's ToggleButtonGroup unchanged. `auto` is new: it defers to the
 * spine's own width instead of the window's.
 */
export type SpineViewMode = 'compact' | 'two-column' | 'auto';

function GroupedCard({ group, ctx }: { group: CandidateGroup; ctx: SpineContext }) {
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
                  <Typography
                    variant="body2"
                    sx={{
                      fontWeight: 'bold',
                    }}
                  >
                    {r.book.title}
                  </Typography>
                  <Stack
                    direction="row"
                    spacing={0.5}
                    sx={{
                      flexWrap: 'wrap',
                    }}
                  >
                    {r.book.format && <Chip label={r.book.format} size="small" />}
                    {r.book.duration_seconds && (
                      <Typography variant="caption">
                        {formatDuration(r.book.duration_seconds)}
                      </Typography>
                    )}
                    {r.book.file_size_bytes && (
                      <Typography variant="caption">
                        · {formatFileSize(r.book.file_size_bytes)}
                      </Typography>
                    )}
                  </Stack>
                  <Typography
                    variant="caption"
                    sx={{
                      display: 'block',
                      wordBreak: 'break-all',
                      color: 'text.secondary',
                    }}
                  >
                    {r.book.file_path}
                  </Typography>
                  {r.book.itunes_path && (
                    <Typography
                      variant="caption"
                      sx={{
                        color: 'info.main',
                        display: 'block',
                        wordBreak: 'break-all',
                      }}
                    >
                      iTunes: {r.book.itunes_path}
                    </Typography>
                  )}
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
                  label={`${Math.round(c.score * 100)}%`}
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
                    color="success"
                    onClick={() =>
                      ctx.onAction({ lane: 'metadata', type: 'applySelected', ids: actionableIds })
                    }
                  >
                    Apply All ({actionableIds.length})
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

function CompactRow({ r, ctx }: { r: CandidateResult; ctx: SpineContext }) {
  const bookId = r.book.id;
  const isExpanded = ctx.expandedId === bookId;

  return (
    <Box key={bookId}>
      <Stack
        direction="row"
        spacing={1}
        onClick={() => ctx.onToggleExpand(bookId)}
        sx={{
          alignItems: 'center',
          p: 1,
          cursor: 'pointer',
          '&:hover': { bgcolor: 'action.hover' },
          ...getRowSx(ctx.rowState(bookId)),
        }}
      >
        <Checkbox
          size="small"
          checked={ctx.isSelected(bookId)}
          onClick={(e) => e.stopPropagation()}
          onChange={() => ctx.onToggleSelect(bookId)}
          disabled={!isRowActionable(ctx.rowState(bookId))}
        />
        <Avatar
          src={r.candidate?.cover_url || r.book.cover_url || ''}
          variant="rounded"
          sx={{ width: 40, height: 50, cursor: 'pointer' }}
          onClick={(e) => {
            e.stopPropagation();
            ctx.onPreviewCover(r.candidate?.cover_url || r.book.cover_url || '');
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
            ) : r.status === 'no_match' ? (
              <Chip label="No match" size="small" sx={{ ml: 1 }} />
            ) : r.status === 'error' ? (
              <Chip label="Error" size="small" color="error" sx={{ ml: 1 }} />
            ) : null}
          </Typography>
        </Box>
        {r.candidate && (
          <>
            <Chip
              label={`${Math.round(r.candidate.score * 100)}%`}
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
            {Math.abs(r.candidate?.duration_delta_sec ?? 0) > 600 && (
              <Chip
                label={`⚠ runtime differs by ${formatDuration(Math.abs(r.candidate.duration_delta_sec!))}`}
                color="warning"
                size="small"
                sx={{ fontWeight: 500 }}
              />
            )}
          </>
        )}
        {isRowActionable(ctx.rowState(bookId)) && r.candidate && (
          <>
            <Button
              size="small"
              variant="contained"
              color="success"
              onClick={(e) => {
                e.stopPropagation();
                ctx.onAction({ lane: 'metadata', type: 'apply', id: bookId });
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
                ctx.onAction({ lane: 'metadata', type: 'reject', id: bookId });
              }}
            >
              Reject
            </Button>
            <Button
              size="small"
              variant="text"
              onClick={(e) => {
                e.stopPropagation();
                ctx.onAction({ lane: 'metadata', type: 'skip', id: bookId });
              }}
            >
              Skip
            </Button>
          </>
        )}
        {ctx.rowState(bookId) === 'skipped' && (
          <Chip
            label="Skipped"
            size="small"
            onClick={(e) => {
              e.stopPropagation();
              ctx.onAction({ lane: 'metadata', type: 'unskip', id: bookId });
            }}
            sx={{ cursor: 'pointer' }}
          />
        )}
        {ctx.rowState(bookId) === 'applied' && (
          <Chip label="Applied" size="small" color="success" />
        )}
        {ctx.rowState(bookId) === 'rejected' && (
          <Chip
            label="Rejected — click to undo"
            size="small"
            color="error"
            onClick={(e) => {
              e.stopPropagation();
              ctx.onAction({ lane: 'metadata', type: 'unreject', id: bookId });
            }}
            sx={{ cursor: 'pointer' }}
          />
        )}
      </Stack>

      {/* Expanded two-column detail for this row */}
      {isExpanded && r.candidate && (
        <Box sx={{ p: 2, pl: 7, bgcolor: 'action.hover', borderRadius: 1 }}>
          <Stack direction="row" spacing={2}>
            <Box sx={{ flex: 1 }}>
              <Typography variant="subtitle2" gutterBottom>
                Current
              </Typography>
              <Stack
                direction="row"
                spacing={1}
                sx={{
                  alignItems: 'flex-start',
                }}
              >
                <Avatar
                  src={r.book.cover_url || ''}
                  variant="rounded"
                  sx={{ width: 60, height: 80, cursor: r.book.cover_url ? 'pointer' : 'default' }}
                  onClick={() => r.book.cover_url && ctx.onPreviewCover(r.book.cover_url)}
                />
                <Box>
                  <Typography
                    variant="body2"
                    sx={{
                      fontWeight: 'bold',
                    }}
                  >
                    {r.book.title}
                  </Typography>
                  <Typography variant="body2">{r.book.author}</Typography>
                  {r.book.format && <Chip label={r.book.format} size="small" sx={{ mt: 0.5 }} />}
                  {r.book.duration_seconds && (
                    <Typography
                      variant="caption"
                      sx={{
                        display: 'block',
                      }}
                    >
                      {formatDuration(r.book.duration_seconds)}
                    </Typography>
                  )}
                  {r.book.file_size_bytes && (
                    <Typography
                      variant="caption"
                      sx={{
                        display: 'block',
                      }}
                    >
                      {formatFileSize(r.book.file_size_bytes)}
                    </Typography>
                  )}
                  <Typography variant="caption" sx={{ wordBreak: 'break-all' }}>
                    {r.book.file_path}
                  </Typography>
                  {r.book.itunes_path && (
                    <Typography
                      variant="caption"
                      sx={{
                        color: 'info.main',
                        display: 'block',
                        wordBreak: 'break-all',
                      }}
                    >
                      iTunes: {r.book.itunes_path}
                    </Typography>
                  )}
                </Box>
              </Stack>
            </Box>
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
                    r.candidate?.cover_url && ctx.onPreviewCover(r.candidate.cover_url)
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
                    label={`${Math.round(r.candidate.score * 100)}%`}
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
          </Stack>
          <EvidenceSection candidate={r.candidate} />
        </Box>
      )}
    </Box>
  );
}

function TwoColumnCard({ r, ctx }: { r: CandidateResult; ctx: SpineContext }) {
  const bookId = r.book.id;

  return (
    <Box
      key={bookId}
      sx={{
        p: 2,
        mb: 1,
        border: 1,
        borderColor: 'divider',
        ...getRowSx(ctx.rowState(bookId)),
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
              checked={ctx.isSelected(bookId)}
              onChange={() => ctx.onToggleSelect(bookId)}
              disabled={!isRowActionable(ctx.rowState(bookId))}
            />
            <Avatar
              src={r.book.cover_url || ''}
              variant="rounded"
              sx={{ width: 60, height: 80, cursor: r.book.cover_url ? 'pointer' : 'default' }}
              onClick={() => r.book.cover_url && ctx.onPreviewCover(r.book.cover_url)}
            />
            <Box sx={{ minWidth: 0 }}>
              <Typography
                variant="body2"
                sx={{
                  fontWeight: 'bold',
                }}
              >
                {r.book.title}
              </Typography>
              <Typography variant="body2">{r.book.author}</Typography>
              {r.book.format && <Chip label={r.book.format} size="small" sx={{ mt: 0.5 }} />}
              {r.book.duration_seconds && (
                <Typography
                  variant="caption"
                  sx={{
                    display: 'block',
                  }}
                >
                  {formatDuration(r.book.duration_seconds)}
                </Typography>
              )}
              {r.book.file_size_bytes && (
                <Typography
                  variant="caption"
                  sx={{
                    display: 'block',
                  }}
                >
                  {formatFileSize(r.book.file_size_bytes)}
                </Typography>
              )}
              <Typography variant="caption" sx={{ wordBreak: 'break-all' }}>
                {r.book.file_path}
              </Typography>
              {r.book.itunes_path && (
                <Typography
                  variant="caption"
                  sx={{
                    color: 'info.main',
                    display: 'block',
                    wordBreak: 'break-all',
                  }}
                >
                  iTunes: {r.book.itunes_path}
                </Typography>
              )}
            </Box>
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
                onClick={() => r.candidate?.cover_url && ctx.onPreviewCover(r.candidate.cover_url)}
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
                    label={`${Math.round(r.candidate.score * 100)}%`}
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
                {isRowActionable(ctx.rowState(bookId)) && (
                  <Stack direction="row" spacing={1} sx={{ mt: 1 }}>
                    <Button
                      size="small"
                      variant="contained"
                      color="success"
                      onClick={() => ctx.onAction({ lane: 'metadata', type: 'apply', id: bookId })}
                    >
                      Apply
                    </Button>
                    <Button
                      size="small"
                      variant="outlined"
                      color="error"
                      onClick={() => ctx.onAction({ lane: 'metadata', type: 'reject', id: bookId })}
                    >
                      Reject
                    </Button>
                    <Button
                      size="small"
                      variant="text"
                      onClick={() => ctx.onAction({ lane: 'metadata', type: 'skip', id: bookId })}
                    >
                      Skip
                    </Button>
                  </Stack>
                )}
                {ctx.rowState(bookId) === 'skipped' && (
                  <Chip
                    label="Skipped — click to undo"
                    size="small"
                    onClick={() => ctx.onAction({ lane: 'metadata', type: 'unskip', id: bookId })}
                    sx={{ cursor: 'pointer', mt: 1 }}
                  />
                )}
                {ctx.rowState(bookId) === 'rejected' && (
                  <Chip
                    label="Rejected — click to undo"
                    size="small"
                    color="error"
                    onClick={() => ctx.onAction({ lane: 'metadata', type: 'unreject', id: bookId })}
                    sx={{ cursor: 'pointer', mt: 1 }}
                  />
                )}
                {ctx.rowState(bookId) === 'applied' && (
                  <Chip label="Applied" size="small" color="success" sx={{ mt: 1 }} />
                )}
              </Box>
            </Stack>
          ) : (
            <Box sx={{ display: 'flex', alignItems: 'center', height: '100%' }}>
              <Chip
                label={
                  r.status === 'no_match'
                    ? 'No match found'
                    : `Error: ${r.error_message || 'Unknown'}`
                }
                color={r.status === 'error' ? 'error' : 'default'}
              />
            </Box>
          )}
        </Box>
      </Stack>
      {r.candidate && <EvidenceSection candidate={r.candidate} />}
    </Box>
  );
}

/**
 * Width at which `auto` shows the two-column comparison.
 *
 * 700px is the spine's OWN width, not the viewport's -- which is the entire
 * point. The dialog's two-column card is a `Stack direction="row"` with
 * `flex: 1 / flex: 1` and no responsive collapse at any width: put it beside a
 * queue rail on a laptop and both columns squish rather than stacking. A media
 * query cannot fix that, because the window can be wide while the spine is not.
 */
export const SPINE_TWO_COLUMN_MIN = 700;

/**
 * The comparison surface.
 *
 * Grouped results always render as grouped cards regardless of view mode: a
 * group is several books competing for ONE candidate, and there is no honest way
 * to show that as a row per book -- each row would repeat the same candidate and
 * imply each book had its own match.
 */
export function CompareSpine({
  rows,
  groups = [],
  viewMode,
  ctx,
  emptyMessage = 'Nothing to compare.',
}: {
  rows: CandidateResult[];
  groups?: CandidateGroup[];
  viewMode: SpineViewMode;
  ctx: SpineContext;
  emptyMessage?: string;
}) {
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
        // The spine IS the container the auto-mode query resolves against. This
        // declaration has to be on this element: put it on the row and
        // `@container` measures the row, which is already as wide as the spine,
        // and the collapse never fires.
        containerType: 'inline-size',
        containerName: 'spine',
      }}
    >
      {groups.map((group) => (
        <GroupedCard key={group.key} group={group} ctx={ctx} />
      ))}

      {rows.map((r) =>
        viewMode === 'compact' ? (
          <CompactRow key={r.book.id} r={r} ctx={ctx} />
        ) : viewMode === 'two-column' ? (
          <TwoColumnCard key={r.book.id} r={r} ctx={ctx} />
        ) : (
          <AutoCard key={r.book.id} r={r} ctx={ctx} />
        )
      )}
    </Box>
  );
}

/**
 * `auto`: the two-column card, collapsed to a single column when the SPINE is
 * narrow.
 *
 * Implemented by rendering the same TwoColumnCard inside a wrapper that flips
 * its inner `Stack` from row to column via a container query, rather than by
 * forking the renderer. Forking would double the surface that Phase 7's
 * inventory has to check, and the two copies would drift.
 *
 * jsdom does not evaluate container queries, so a unit test can assert that the
 * rule is emitted but not that the collapse happens. Whether it actually
 * reflows is a browser-level question and belongs to the visual harness -- the
 * same split used for the theme's signal colours.
 */
function AutoCard({ r, ctx }: { r: CandidateResult; ctx: SpineContext }) {
  return (
    <Box
      data-testid="spine-auto-card"
      sx={{
        [`@container spine (max-width: ${SPINE_TWO_COLUMN_MIN - 1}px)`]: {
          '& > div > .MuiStack-root': {
            flexDirection: 'column',
          },
        },
      }}
    >
      <TwoColumnCard r={r} ctx={ctx} />
    </Box>
  );
}
