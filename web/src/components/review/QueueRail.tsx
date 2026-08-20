// file: web/src/components/review/QueueRail.tsx
// version: 1.3.0
// guid: 4f8c2b96-7a15-4e30-9d82-6b0e5a3c1f74
// last-edited: 2026-08-20
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

import {
  Badge,
  Box,
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
import type { CandidateResult } from '../../services/api';
import { PAGE_SIZE_OPTIONS, type MetadataFilters } from './lanes/useMetadataLane';
import { isDecided, scoreColor, type RowState } from './spine/rowState';

/** Provider chips, in the inventory's order. */
const PROVIDERS: Array<{ id: string; label: string }> = [
  { id: 'audible', label: 'Audible' },
  { id: 'google_books', label: 'Google Books' },
  { id: 'openlibrary', label: 'Open Library' },
];

/**
 * The eight filter switches.
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
  };
  sourceCounts: Record<string, number>;
  filters: MetadataFilters;
  setFilters: (patch: Partial<MetadataFilters>) => void;
  strictPreset: boolean;
  setStrictPreset: (on: boolean) => void;
  page: number;
  totalPages: number;
  pageSize: number;
  setPage: (p: number) => void;
  setPageSize: (n: number) => void;
  filteredCount: number;
  rowState: (id: string) => RowState | undefined;
  isSelected: (id: string) => boolean;
  onToggleSelect: (id: string) => void;
  onRefresh: () => void;
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

export function QueueRail({
  loading,
  rows,
  summary,
  sourceCounts,
  filters,
  setFilters,
  strictPreset,
  setStrictPreset,
  page,
  totalPages,
  pageSize,
  setPage,
  setPageSize,
  filteredCount,
  rowState,
  isSelected,
  onToggleSelect,
  onRefresh,
}: QueueRailProps) {
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
        {/* Summary chips — matched / no match / errors, against total. */}
        <Stack direction="row" spacing={0.5} useFlexGap sx={{ flexWrap: 'wrap' }}>
          <Chip size="small" color="success" label={`${summary.matched} matched`} />
          <Chip size="small" label={`${summary.no_match} no match`} />
          {summary.errors > 0 && (
            <Chip size="small" color="error" label={`${summary.errors} errors`} />
          )}
          <Chip size="small" variant="outlined" label={`${summary.total} total`} />
          {summary.stale > 0 && (
            // Stale rows ARE reviewable and are counted in `total` -- this is
            // not a shortfall, it is a caveat on what is already in the list.
            // MetadataCacheTTL's contract says stale entries stay readable and
            // the UI flags them; before this the review surface received no age
            // at all, so month-old candidates looked freshly fetched.
            <Tooltip
              title={`${summary.stale.toLocaleString()} of these were fetched more than 30 days ago. They are still reviewable, but the source may have changed since — refetch to be sure.`}
            >
              <Chip
                size="small"
                variant="outlined"
                color="warning"
                icon={<HistoryIcon fontSize="small" />}
                label={`${summary.stale.toLocaleString()} stale`}
              />
            </Tooltip>
          )}
          {summary.unreviewable > 0 && (
            // `total` counts only what a reviewer can act on. This says what the
            // cache holds that they cannot, so the difference is stated rather
            // than left as a silent shortfall.
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

        <FormControlLabel
          control={
            <Switch
              size="small"
              checked={strictPreset}
              onChange={(e) => setStrictPreset(e.target.checked)}
              slotProps={{ input: { 'aria-label': 'Strict review preset' } }}
            />
          }
          label={<Typography variant="body2">Strict review</Typography>}
        />

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
        <Typography
          variant="caption"
          sx={{ px: 1.5, py: 1, display: 'block' }}
          color="text.secondary"
        >
          {loading ? 'Loading…' : `${filteredCount} shown`}
        </Typography>
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
                  {r.candidate && (
                    <Typography variant="caption" color="text.secondary" noWrap>
                      {r.candidate.title}
                    </Typography>
                  )}
                </Box>
                {/* Explicitly false, not falsy: a row with no age is not a
                    stale row, and marking it as one would be a claim the
                    payload never made. */}
                {r.is_fresh === false && (
                  <Tooltip title={staleRowTitle(r.fetched_at)}>
                    <Box
                      component="span"
                      sx={{ display: 'flex' }}
                      aria-label={staleRowTitle(r.fetched_at)}
                    >
                      <HistoryIcon fontSize="small" color="warning" />
                    </Box>
                  </Tooltip>
                )}
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
