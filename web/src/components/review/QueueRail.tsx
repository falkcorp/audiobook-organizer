// file: web/src/components/review/QueueRail.tsx
// version: 1.11.0
// guid: 4f8c2b96-7a15-4e30-9d82-6b0e5a3c1f74
// last-edited: 2026-09-30
//
// The left rail: everything that decides WHICH candidates are in front of the
// reviewer, plus a queue overview of the ones that made it through.
//
// PAGINATION, NOT VIRTUALIZATION -- A DELIBERATE CHOICE
//
// PLAN.md calls this a "virtualized candidate list", and the feature inventory
// separately requires pagination with a persisted page size. Those are two
// answers to the same problem and building both means a virtualizer scrolling
// inside a paginator, where the window is already capped.
//
// Pagination wins because it is the one the inventory mandates, it is what the
// persisted `METADATA_REVIEW_PAGE_SIZE` exists to serve, and it caps the list at
// 100 rows -- the size at which a virtualizer starts costing more than it saves.
// If page size ever grows past a few hundred, revisit this; the note is here so
// that is a decision rather than a discovery.
//
// The selection highlight uses `:has(input:checked)` as PLAN.md specifies, so a
// ticked row is styled by the DOM rather than by a second copy of the selection
// state in a class name. jsdom does not evaluate `:has()`, so the test asserts
// the rule is emitted; whether it paints is a visual-harness question -- the
// same split used for the spine's container query.
//
// EVERY SUMMARY CHIP IS A FILTER (owner request 2026-09-27)
//
// A chip used to be a read-out: "11324 unreviewable" with no way to see which
// books those were. Clicking a chip now shows exactly the books it counts --
// the lane pauses every other filter while one is active, so the number on the
// chip is the number of rows in the list. Clicking it again, or "Show all",
// clears it. Orphaned rows (the book is gone) stay count-only: there is no
// book to show.

import {
  Alert,
  Badge,
  Box,
  Button,
  Chip,
  Divider,
  FormControlLabel,
  IconButton,
  InputAdornment,
  MenuItem,
  Pagination,
  Slider,
  Stack,
  Switch,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import ClearIcon from '@mui/icons-material/Clear';
import RefreshIcon from '@mui/icons-material/Refresh';
import HistoryIcon from '@mui/icons-material/History';
import SearchIcon from '@mui/icons-material/Search';
import type { CandidateResult } from '../../services/api';
import {
  PAGE_SIZE_OPTIONS,
  REVIEW_LEVELS,
  REVIEW_LEVEL_LABELS,
  isUnreviewableRow,
  reviewLevelFilters,
  type ChipFilter,
  type MetadataFilters,
  type ReviewLevel,
} from './lanes/useMetadataLane';
import { isDecided, scoreColor, type RowState } from './spine/rowState';

/** Provider chips, in the inventory's order. */
const PROVIDERS: Array<{ id: string; label: string }> = [
  { id: 'audible', label: 'Audible' },
  { id: 'google_books', label: 'Google Books' },
  { id: 'openlibrary', label: 'Open Library' },
];

/**
 * The nine filter switches.
 *
 * Two of these have tooltips that state BEHAVIOUR rather than describing the
 * control, and they are carried verbatim from the dialog because the
 * information is not recoverable from the code:
 *
 *  - hideMultiBook also removes the hidden books from Apply Selected.
 *  - onlyWithTranscription and onlyTranscriptionMatched are different questions:
 *    "has Whisper data" vs "the score was boosted by it".
 */
const SWITCHES: Array<{ key: keyof MetadataFilters; label: string; help?: string }> = [
  { key: 'hideApplied', label: 'Hide applied' },
  { key: 'hideRejected', label: 'Hide rejected' },
  { key: 'hideSkipped', label: 'Hide skipped' },
  { key: 'hideNoMatch', label: 'Hide no-match' },
  {
    key: 'hideRuntimeDifferences',
    label: 'Hide runtime differences',
    help:
      'Hide every candidate showing the "runtime differs" warning (more than 10 ' +
      "minutes off the book's runtime) and any candidate more than 10% off, which " +
      'the apply gate refuses. Rows with an unknown runtime on either side stay ' +
      'visible. Part of In-depth and Strict review.',
  },
  {
    key: 'hideMultiBook',
    label: 'Hide multi-book matches',
    help:
      'Hide any book that shares a match with another book. Turning this on leaves only ' +
      'the straightforward one-book-one-match rows, and takes the hidden books out of ' +
      'Apply Selected too.',
  },
  {
    key: 'matchLanguage',
    label: 'Match language',
    help: 'Books without a language set still show all candidates.',
  },
  {
    key: 'onlyWithTranscription',
    label: 'Has transcription',
    help: 'Only books with Whisper intro data.',
  },
  {
    key: 'onlyTranscriptionMatched',
    label: 'Transcription matched',
    help: 'Only books whose score was boosted by the transcription — not the same as “has transcription”.',
  },
];

export interface QueueRailProps {
  loading: boolean;
  rows: CandidateResult[];
  summary: {
    matched: number;
    no_match: number;
    errors: number;
    total: number;
    unreviewable: number;
    stale: number;
    unreviewable_by_cause?: { orphaned: number; no_candidates: number; decode_errors: number };
    /**
     * Books already ruled on that have no candidate left to show. Not a backlog
     * and not an error, so they are no longer counted in `unreviewable` — but
     * they are still reported, because a number that belongs to no bucket is a
     * number nobody can act on.
     */
    resolved_no_candidates?: number;
  };
  sourceCounts: Record<string, number>;
  filters: MetadataFilters;
  setFilters: (patch: Partial<MetadataFilters>) => void;
  reviewLevel: ReviewLevel;
  setReviewLevel: (level: ReviewLevel) => void;
  /** True when a switch was flipped by hand after the level was picked. */
  levelCustomised?: boolean;
  /** How many rows Hide runtime differences is hiding right now. */
  runtimeHiddenCount?: number;
  /** The chip whose books the list is showing, or null. */
  chipFilter?: ChipFilter | null;
  /** Toggle a chip's filter. Optional: without it the chips are read-outs. */
  onToggleChip?: (chip: ChipFilter) => void;
  onClearChip?: () => void;
  /** The unreviewable bucket (no candidate / decode error) is loading. */
  unreviewableLoading?: boolean;
  unreviewableError?: string | null;
  page: number;
  totalPages: number;
  pageSize: number;
  setPage: (p: number) => void;
  setPageSize: (n: number) => void;
  filteredCount: number;
  rowState: (id: string) => RowState | undefined;
  isSelected: (id: string) => boolean;
  onToggleSelect: (id: string) => void;
  /**
   * Select or deselect every row on this page at once. Optional: without it
   * the list has no select-all box. Selecting thousands of no-candidate books
   * one checkbox at a time is not a workflow.
   */
  onSelectPage?: (ids: string[], selected: boolean) => void;
  /**
   * Gmail's pattern: once the whole page is ticked, offer every book the view
   * matches across all pages. The lane holds every matching row client-side
   * (it loads with all=true), so this needs no request.
   */
  onSelectAllMatching?: () => void;
  onClearSelection?: () => void;
  allMatchingSelected?: boolean;
  selectedCount?: number;
  onRefresh: () => void;
  /**
   * Refetch every stale row in the library, not just the ones on this page.
   * Optional so the rail still renders without a refetch path wired up; when
   * absent the stale chip stays a plain read-out, which is what it was before.
   */
  onRefetchStale?: () => void;
  /** Refetch one row. */
  onRefetchRow?: (bookId: string) => void;
  /**
   * Raised when the reviewer wants to search for this book with a query they
   * type themselves.
   *
   * Distinct from onRefetchRow, which re-runs the AUTOMATIC fetch using the
   * book's own tags. That is exactly what already failed on a no_match row: if
   * the tags are wrong -- an author field holding a release-group tag or a book
   * title -- refetching asks the same bad question again and gets the same
   * answer. A human-supplied query is the only thing that moves those rows.
   */
  onSearchRow?: (bookId: string) => void;
  /** Disables both refetch affordances while a request is in flight. */
  refetching?: boolean;
}

/**
 * Describe WHY rows are unreviewable, not just how many.
 *
 * The causes call for opposite remedies -- a row whose book is gone can only be
 * reaped, a row with no stored candidate can be refetched -- so a reader given
 * only the total has no way to tell which they are looking at. On production
 * that total read 8,532 and said nothing about the 3,354/5,178 split inside it.
 *
 * A server that does not send the breakdown falls back to naming the causes
 * without counting them, which is exactly what this tooltip said before.
 */
/**
 * How old a cached candidate is, in whole days, or null when the row carries no
 * usable timestamp.
 *
 * Null rather than a guess: the row already knows it is stale from `is_fresh`,
 * and an invented age is worse than an unspecified one.
 */
export function daysSince(iso?: string): number | null {
  if (!iso) return null;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return null;
  return Math.max(0, Math.floor((Date.now() - t) / 86_400_000));
}

/** Tooltip for the per-row stale marker. */
export function staleRowTitle(fetchedAt?: string): string {
  const days = daysSince(fetchedAt);
  const age = days === null ? 'more than 30 days ago' : `${days.toLocaleString()} days ago`;
  return `Fetched ${age} \u2014 the source may have changed since. Refetch to be sure.`;
}

export function unreviewableReason(byCause?: {
  orphaned: number;
  no_candidates: number;
  decode_errors: number;
}): string {
  if (!byCause) {
    return 'Cache entries with no candidate stored, or whose book no longer exists. Nothing here can be reviewed.';
  }
  const parts: string[] = [];
  if (byCause.orphaned > 0) {
    parts.push(
      `${byCause.orphaned.toLocaleString()} whose book no longer exists \u2014 only a cleanup pass clears these`
    );
  }
  if (byCause.no_candidates > 0) {
    parts.push(
      `${byCause.no_candidates.toLocaleString()} with no candidate stored \u2014 a refetch would fill these in`
    );
  }
  if (byCause.decode_errors > 0) {
    parts.push(`${byCause.decode_errors.toLocaleString()} whose stored candidate will not decode`);
  }
  if (parts.length === 0) return 'Nothing here can be reviewed.';
  return `Nothing here can be reviewed: ${parts.join('; ')}.`;
}

/** What turning on this level does, for the slider stop's tooltip. */
function reviewLevelDescription(level: ReviewLevel): string {
  if (level === 'off') return 'No preset: minimum confidence 85%, nothing extra hidden.';
  const f = reviewLevelFilters(level);
  const parts = [
    `Minimum confidence ${f.confidenceThreshold}%`,
    'hide skipped',
    'hide multi-book matches',
  ];
  if (f.hideRuntimeDifferences) parts.push('hide runtime differences');
  if (f.onlyWithTranscription) parts.push('only books with a transcription');
  if (f.onlyTranscriptionMatched) parts.push('only transcription-matched');
  return `${REVIEW_LEVEL_LABELS[level]}: ${parts.join(', ')}.`;
}

/** Human label for the active chip, for the "showing" banner. */
const CHIP_LABELS: Record<ChipFilter, string> = {
  matched: 'matched',
  no_match: 'no match',
  total: 'reviewable',
  errors: 'error',
  no_candidates: 'no-candidate',
  resolved_no_candidates: 'resolved, no-candidate',
  stale: 'stale',
};

export function QueueRail({
  loading,
  rows,
  summary,
  sourceCounts,
  filters,
  setFilters,
  reviewLevel,
  setReviewLevel,
  levelCustomised = false,
  runtimeHiddenCount = 0,
  chipFilter = null,
  onToggleChip,
  onClearChip,
  unreviewableLoading = false,
  unreviewableError = null,
  page,
  totalPages,
  pageSize,
  setPage,
  setPageSize,
  filteredCount,
  rowState,
  isSelected,
  onToggleSelect,
  onSelectPage,
  onSelectAllMatching,
  onClearSelection,
  allMatchingSelected = false,
  selectedCount = 0,
  onRefresh,
  onRefetchStale,
  onRefetchRow,
  onSearchRow,
  refetching = false,
}: QueueRailProps) {
  // A chip's props when it filters: clickable, highlighted while active, and
  // pressed-state exposed to assistive tech. Without onToggleChip the chips
  // stay read-outs, which is what they were.
  const chipProps = (chip: ChipFilter, count: number) =>
    onToggleChip
      ? {
          clickable: true,
          onClick: () => onToggleChip(chip),
          variant: chipFilter === chip ? ('filled' as const) : ('outlined' as const),
          'aria-pressed': chipFilter === chip,
          'aria-label': `Show the ${count.toLocaleString()} ${CHIP_LABELS[chip]} books`,
          'data-testid': `chip-${chip}`,
        }
      : { variant: 'outlined' as const, 'data-testid': `chip-${chip}` };
  const byCause = summary.unreviewable_by_cause;
  const levelIndex = Math.max(0, REVIEW_LEVELS.indexOf(reviewLevel));
  return (
    <Box
      data-testid="queue-rail"
      component="aside"
      aria-label="Review queue and filters"
      sx={{
        display: 'flex',
        flexDirection: 'column',
        minHeight: 0,
        borderRight: 1,
        borderColor: 'divider',
        bgcolor: 'background.paper',
      }}
    >
      <Stack spacing={1.5} sx={{ p: 1.5, overflowY: 'auto' }}>
        {/* Summary chips. Every one with books behind it is a filter. */}
        <Stack direction="row" spacing={0.5} useFlexGap sx={{ flexWrap: 'wrap' }}>
          <Chip
            size="small"
            color="success"
            label={`${summary.matched.toLocaleString()} matched`}
            {...chipProps('matched', summary.matched)}
          />
          <Chip
            size="small"
            label={`${summary.no_match.toLocaleString()} no match`}
            {...chipProps('no_match', summary.no_match)}
          />
          <Chip
            size="small"
            label={`${summary.total.toLocaleString()} total`}
            {...chipProps('total', summary.total)}
          />
          {summary.stale > 0 && (
            // Stale rows ARE reviewable (and the no-candidate ones refetchable)
            // -- this is a caveat on age, not a shortfall. The chip shows the
            // stale books; refetching them all is the button beside it, so
            // looking at them no longer starts thousands of provider calls.
            <Tooltip
              title={
                `${summary.stale.toLocaleString()} books were last searched more than 30 days ago ` +
                '(books you marked no match are not counted: they are never searched again). ' +
                'They are still reviewable, but the source may have changed since. Click to see them.'
              }
            >
              <Chip
                size="small"
                color="warning"
                icon={<HistoryIcon fontSize="small" />}
                label={`${summary.stale.toLocaleString()} stale`}
                {...chipProps('stale', summary.stale)}
              />
            </Tooltip>
          )}
          {summary.stale > 0 && onRefetchStale && (
            <Tooltip title="Refetch every stale book from the providers.">
              {/* A disabled button does not fire the events Tooltip needs. */}
              <Box component="span" sx={{ display: 'inline-flex' }}>
                <IconButton
                  size="small"
                  color="warning"
                  disabled={refetching}
                  onClick={onRefetchStale}
                  aria-label={`Refetch ${summary.stale.toLocaleString()} stale books`}
                >
                  <RefreshIcon fontSize="small" />
                </IconButton>
              </Box>
            </Tooltip>
          )}
          {summary.errors > 0 && (
            // Errors get their OWN chip, not a line buried in the unreviewable
            // tooltip. A stored candidate that will not decode is a broken row
            // someone has to repair; a book the providers simply have nothing
            // for is normal and needs no attention at all.
            <Tooltip title="Cache rows whose stored candidate will not decode. These are broken, not merely unmatched — a search again is the usual repair.">
              <Chip
                size="small"
                color="error"
                label={`${summary.errors.toLocaleString()} errors`}
                {...chipProps('errors', summary.errors)}
              />
            </Tooltip>
          )}
          {byCause && byCause.no_candidates > 0 && (
            <Tooltip title="Nobody has ruled on these books and no candidate is stored for them. Select them and use Search again to ask the providers with each book's title and author.">
              <Chip
                size="small"
                color="warning"
                label={`${byCause.no_candidates.toLocaleString()} no candidates`}
                {...chipProps('no_candidates', byCause.no_candidates)}
              />
            </Tooltip>
          )}
          {Boolean(summary.resolved_no_candidates) && (
            <Tooltip
              title={`${(summary.resolved_no_candidates ?? 0).toLocaleString()} books already matched or marked no-match whose stored candidate is gone. Nothing to do — they are counted here rather than as a backlog.`}
            >
              <Chip
                size="small"
                label={`${(summary.resolved_no_candidates ?? 0).toLocaleString()} resolved, no candidate`}
                {...chipProps('resolved_no_candidates', summary.resolved_no_candidates ?? 0)}
              />
            </Tooltip>
          )}
          {byCause && byCause.orphaned > 0 && (
            // Count-only on purpose: the book these rows point at no longer
            // exists, so there is nothing to list.
            <Tooltip title="Cache rows whose book no longer exists. There is no book to show — only a cleanup pass clears these.">
              <Chip
                size="small"
                variant="outlined"
                label={`${byCause.orphaned.toLocaleString()} orphaned`}
                data-testid="chip-orphaned"
              />
            </Tooltip>
          )}
          {!byCause && summary.unreviewable > 0 && (
            // An older server sends no per-cause split, so there is no bucket
            // to filter by: the total stays a read-out.
            <Tooltip title={unreviewableReason(summary.unreviewable_by_cause)}>
              <Chip
                size="small"
                variant="outlined"
                color="warning"
                label={`${summary.unreviewable} unreviewable`}
              />
            </Tooltip>
          )}
          <Tooltip title="Reload the review set">
            <IconButton size="small" onClick={onRefresh} aria-label="Refresh review set">
              <RefreshIcon fontSize="small" />
            </IconButton>
          </Tooltip>
        </Stack>

        {chipFilter && (
          <Alert
            severity="info"
            data-testid="chip-filter-banner"
            action={
              onClearChip && (
                <Button color="inherit" size="small" onClick={onClearChip}>
                  Show all
                </Button>
              )
            }
          >
            {unreviewableLoading
              ? `Loading the ${CHIP_LABELS[chipFilter]} books…`
              : `Showing the ${filteredCount.toLocaleString()} ${CHIP_LABELS[chipFilter]} books. Other filters are paused.`}
          </Alert>
        )}
        {chipFilter && unreviewableError && (
          <Alert severity="error" data-testid="unreviewable-error">
            {unreviewableError}
          </Alert>
        )}

        {/* Review level: four cumulative stops (owner ruling 2026-09-27). */}
        <Box sx={{ px: 1 }}>
          <Typography variant="caption" color="text.secondary" id="review-level-label">
            Review level: {REVIEW_LEVEL_LABELS[reviewLevel]}
            {levelCustomised ? ' (switches changed)' : ''}
          </Typography>
          <Slider
            size="small"
            min={0}
            max={REVIEW_LEVELS.length - 1}
            step={1}
            value={levelIndex}
            marks={REVIEW_LEVELS.map((l, i) => ({
              value: i,
              label: (
                <Tooltip title={reviewLevelDescription(l)}>
                  <span>{l === 'off' ? 'Off' : REVIEW_LEVEL_LABELS[l].replace(' review', '')}</span>
                </Tooltip>
              ),
            }))}
            onChange={(_, v) => {
              const next = REVIEW_LEVELS[v as number];
              if (next && next !== reviewLevel) setReviewLevel(next);
            }}
            getAriaValueText={(v) => REVIEW_LEVEL_LABELS[REVIEW_LEVELS[v] ?? 'off']}
            slotProps={{ input: { 'aria-label': 'Review level' } }}
          />
          {filters.hideRuntimeDifferences && runtimeHiddenCount > 0 && !chipFilter && (
            <Typography variant="caption" color="text.secondary" data-testid="runtime-hidden-count">
              {runtimeHiddenCount.toLocaleString()} hidden by runtime differences
            </Typography>
          )}
        </Box>

        <TextField
          size="small"
          fullWidth
          label="Title filter"
          value={filters.titleFilter}
          onChange={(e) => setFilters({ titleFilter: e.target.value })}
          placeholder="regex"
          slotProps={{
            input: {
              endAdornment: filters.titleFilter ? (
                <InputAdornment position="end">
                  <IconButton
                    size="small"
                    aria-label="Clear title filter"
                    onClick={() => setFilters({ titleFilter: '' })}
                  >
                    <ClearIcon fontSize="small" />
                  </IconButton>
                </InputAdornment>
              ) : null,
            },
          }}
        />

        {/* Provider filter, with per-provider counts. */}
        <Stack direction="row" spacing={0.5} useFlexGap sx={{ flexWrap: 'wrap' }}>
          <Chip
            size="small"
            label="All"
            color={filters.sourceFilter === null ? 'primary' : 'default'}
            onClick={() => setFilters({ sourceFilter: null })}
          />
          {PROVIDERS.map((p) => (
            <Chip
              key={p.id}
              size="small"
              label={`${p.label} (${sourceCounts[p.id] ?? 0})`}
              color={filters.sourceFilter === p.id ? 'primary' : 'default'}
              onClick={() =>
                setFilters({ sourceFilter: filters.sourceFilter === p.id ? null : p.id })
              }
            />
          ))}
        </Stack>

        <Box>
          <Typography variant="caption" color="text.secondary">
            Min confidence: {filters.confidenceThreshold}%
          </Typography>
          <Slider
            size="small"
            min={0}
            max={300}
            value={filters.confidenceThreshold}
            onChange={(_, v) => setFilters({ confidenceThreshold: v as number })}
            aria-label="Minimum confidence"
          />
        </Box>

        <Divider />

        <Box>
          {SWITCHES.map((s) => {
            const control = (
              <FormControlLabel
                key={s.key}
                control={
                  <Switch
                    size="small"
                    checked={Boolean(filters[s.key])}
                    onChange={(e) => setFilters({ [s.key]: e.target.checked })}
                    slotProps={{ input: { 'aria-label': s.label } }}
                  />
                }
                label={<Typography variant="body2">{s.label}</Typography>}
                sx={{ display: 'flex' }}
              />
            );
            return s.help ? (
              <Tooltip key={s.key} title={s.help} placement="right">
                <Box>{control}</Box>
              </Tooltip>
            ) : (
              control
            );
          })}
        </Box>

        <Divider />

        <TextField
          select
          size="small"
          label="Per page"
          value={pageSize}
          onChange={(e) => setPageSize(Number(e.target.value))}
          slotProps={{ htmlInput: { 'aria-label': 'Results per page' } }}
        >
          {PAGE_SIZE_OPTIONS.map((n) => (
            <MenuItem key={n} value={n}>
              {n}
            </MenuItem>
          ))}
        </TextField>
      </Stack>

      <Divider />

      {/* The queue itself. */}
      <Box sx={{ flex: 1, minHeight: 0, overflowY: 'auto' }}>
        <Box sx={{ px: 1.5, py: 1, display: 'flex', alignItems: 'center', gap: 1 }}>
          {onSelectPage && rows.length > 0 && (
            <input
              type="checkbox"
              data-testid="select-page"
              aria-label={`Select all ${rows.length} on this page`}
              checked={rows.every((r) => isSelected(r.book.id))}
              onChange={(e) =>
                onSelectPage(
                  rows.map((r) => r.book.id),
                  e.target.checked
                )
              }
            />
          )}
          <Typography variant="caption" color="text.secondary">
            {loading ? 'Loading…' : `${filteredCount} shown`}
          </Typography>
        </Box>
        {onSelectAllMatching &&
          rows.length > 0 &&
          filteredCount > rows.length &&
          rows.every((r) => isSelected(r.book.id)) && (
            <Alert severity="info" data-testid="select-all-matching-banner" sx={{ mx: 1, mb: 1 }}>
              {allMatchingSelected ? (
                <>
                  All {filteredCount.toLocaleString()} matching selected.{' '}
                  {onClearSelection && (
                    <Button size="small" onClick={onClearSelection} data-testid="clear-selection-banner">
                      Clear selection
                    </Button>
                  )}
                </>
              ) : (
                <>
                  All {rows.length.toLocaleString()} on this page selected
                  {selectedCount > rows.length ? ` (${selectedCount.toLocaleString()} in all)` : ''}.{' '}
                  <Button size="small" onClick={onSelectAllMatching} data-testid="select-all-matching">
                    Select all {filteredCount.toLocaleString()} matching
                  </Button>
                </>
              )}
            </Alert>
          )}
        <Box
          component="ul"
          data-testid="queue-list"
          sx={{
            listStyle: 'none',
            m: 0,
            p: 0,
            // PLAN.md's `:has(input:checked)`: the ticked row is styled from the
            // DOM rather than from a duplicate copy of the selection state.
            '& li:has(input:checked)': {
              bgcolor: 'action.selected',
            },
          }}
        >
          {rows.map((r) => {
            const state = rowState(r.book.id);
            return (
              <Box
                component="li"
                key={r.book.id}
                sx={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 1,
                  px: 1.5,
                  py: 0.75,
                  borderBottom: 1,
                  borderColor: 'divider',
                  opacity: isDecided(state) ? 0.55 : 1,
                }}
              >
                <input
                  type="checkbox"
                  checked={isSelected(r.book.id)}
                  onChange={() => onToggleSelect(r.book.id)}
                  aria-label={`Select ${r.book.title}`}
                />
                <Box sx={{ minWidth: 0, flex: 1 }}>
                  <Typography variant="body2" noWrap title={r.book.title}>
                    {r.book.title}
                  </Typography>
                  {r.candidate ? (
                    <Typography variant="caption" color="text.secondary" noWrap>
                      {r.candidate.title}
                    </Typography>
                  ) : isUnreviewableRow(r) ? (
                    <Typography variant="caption" color="text.secondary" noWrap sx={{ display: 'block' }}>
                      {r.status === 'decode_error' ? 'candidate will not decode' : 'no candidate'}
                      {r.book.author ? ` \u00b7 ${r.book.author}` : ''}
                    </Typography>
                  ) : null}
                </Box>
                {/* Only on rows the automatic fetch could not match. A
                    matched row already has a candidate to review, and offering
                    a manual search there invites re-litigating a decision the
                    reviewer has not made yet. */}
                {(r.status === 'no_match' || isUnreviewableRow(r)) && onSearchRow && (
                  <Tooltip title="No match was found automatically. Search with your own title and author.">
                    <IconButton
                      size="small"
                      aria-label={`Search metadata for ${r.book.title}`}
                      onClick={(e) => {
                        e.stopPropagation();
                        onSearchRow(r.book.id);
                      }}
                    >
                      <SearchIcon fontSize="small" />
                    </IconButton>
                  </Tooltip>
                )}
                {/* Explicitly false, not falsy: a row with no age is not a
                    stale row, and marking it as one would be a claim the
                    payload never made. */}
                {r.is_fresh === false &&
                  (onRefetchRow ? (
                    <Tooltip title={`${staleRowTitle(r.fetched_at)} Click to refetch this book.`}>
                      <IconButton
                        size="small"
                        disabled={refetching}
                        aria-label={`Refetch metadata for ${r.book.title}`}
                        onClick={(e) => {
                          // Defensive, and currently a no-op: the row Box has no
                          // onClick, so selection is driven only by the checkbox
                          // above. Kept because this button sits inside the row's
                          // bounds, and the day the row becomes clickable the
                          // failure would be silent -- refetching a book would
                          // also select it.
                          e.stopPropagation();
                          onRefetchRow(r.book.id);
                        }}
                      >
                        <HistoryIcon fontSize="small" color="warning" />
                      </IconButton>
                    </Tooltip>
                  ) : (
                    <Tooltip title={staleRowTitle(r.fetched_at)}>
                      <Box
                        component="span"
                        sx={{ display: 'flex' }}
                        aria-label={staleRowTitle(r.fetched_at)}
                      >
                        <HistoryIcon fontSize="small" color="warning" />
                      </Box>
                    </Tooltip>
                  ))}
                {r.candidate && (
                  <Chip
                    size="small"
                    color={scoreColor(r.candidate.score)}
                    label={`${Math.round(r.candidate.score * 100)}%`}
                  />
                )}
              </Box>
            );
          })}
        </Box>
      </Box>

      <Divider />
      <Box sx={{ p: 1, display: 'flex', justifyContent: 'center' }}>
        <Badge color="primary" badgeContent={0} invisible>
          <Pagination
            size="small"
            count={totalPages}
            page={page}
            onChange={(_, p) => setPage(p)}
            siblingCount={0}
          />
        </Badge>
      </Box>
    </Box>
  );
}
