// file: web/src/components/review/RegroupPanel.tsx
// version: 1.1.0
// guid: 5a92d0c8-4e17-4b63-8d05-9f2c6a7b1e30
// last-edited: 2026-09-01

/**
 * The regroup lane's full surface: a filter rail and the bucket spine.
 *
 * Assembled here rather than in ReviewWorkspace so the shell keeps its one-line
 * lane branch, matching DupesPanel.
 *
 * ---------------------------------------------------------------------------
 * WHY THERE IS A KIND CONTROL WHEN THE VIEW IS ALREADY GROUPED BY KIND
 * ---------------------------------------------------------------------------
 *
 * This file used to say the opposite -- "there is no filter rail: the queue's
 * only dimension is Kind, and the buckets already ARE that grouping". That
 * reasoning holds for a page that has every row, and this lane does not have
 * every row. The fetch is capped at REGROUP_FETCH_LIMIT, so grouping arranges
 * whatever arrived in one mixed page; the SELECTOR changes what arrives. On the
 * live queue that is the difference between spending the page budget on 484
 * holds of the kind being worked plus 16 of another, and spending all 500 on the
 * kind being worked.
 *
 * So the control is not a client-side re-slice of a grouping. It is the one
 * filter that round-trips, and the reason the cap stops biting as hard.
 *
 * ---------------------------------------------------------------------------
 * THE COUNTS IN THIS RAIL
 * ---------------------------------------------------------------------------
 *
 * Four numbers with four meanings, and the rail keeps them apart deliberately:
 *
 *   "N pending"          the whole queue, all kinds (lane.queueTotal)
 *   "N in <kind>"        that kind on the server, shown only while filtering
 *   "N loaded"           what the lane fetched, shown only when it is short
 *   "showing N of M"     what the search left, shown only while searching
 *
 * The third and the fourth are the pair that must never merge. Truncation is the
 * lane failing to load rows that exist; a search hiding rows is the reviewer
 * asking for that. Rendering them as one number would put a "your view is
 * partial" warning on every keystroke, and a warning that fires constantly is
 * one nobody reads on the occasion it matters.
 */

import {
  Alert,
  Box,
  Button,
  Chip,
  IconButton,
  InputAdornment,
  LinearProgress,
  MenuItem,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import RefreshIcon from '@mui/icons-material/Refresh';
import ClearIcon from '@mui/icons-material/Clear';
import SearchIcon from '@mui/icons-material/Search';
import { RegroupSpine } from './spine/RegroupSpine';
import { labelForKind } from '../../lib/reviewKinds';
import {
  REGROUP_SORTS,
  type RegroupLane,
  type RegroupSort,
} from './lanes/useRegroupLane';

export interface RegroupPanelProps {
  regroup: RegroupLane;
}

export function RegroupPanel({ regroup }: RegroupPanelProps) {
  const { filters, setFilters } = regroup;
  const searching = filters.search.trim().length > 0;

  return (
    <Box sx={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' }}>
      <Box
        data-testid="regroup-rail"
        sx={{
          borderBottom: 1,
          borderColor: 'divider',
          px: 2,
          py: 1,
          display: 'flex',
          flexDirection: 'column',
          gap: 1,
        }}
      >
        <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: 'wrap', alignItems: 'center' }}>
          <TextField
            select
            size="small"
            label="Kind"
            value={filters.kind}
            onChange={(e) => setFilters({ kind: e.target.value })}
            sx={{ minWidth: 220 }}
            data-testid="regroup-kind-select"
            // The aria-label goes on the SELECT slot, not on htmlInput: MUI puts
            // htmlInput on the hidden native input, while the element a reviewer
            // (and a test) actually reaches is the role=combobox div.
            slotProps={{ select: { 'aria-label': 'Filter by kind' } }}
            // Says which way this one goes. Every other control in this rail
            // narrows rows already loaded; this one changes what is fetched, so
            // it is also the only one whose "no results" means "none on the
            // server" rather than "none on this page".
            helperText="Fetched from the server"
          >
            <MenuItem value="">All kinds</MenuItem>
            {regroup.kindOptions.map((k) => (
              <MenuItem key={k.kind} value={k.kind} data-testid={`regroup-kind-option-${k.kind}`}>
                {k.label} ({k.count})
              </MenuItem>
            ))}
          </TextField>

          <TextField
            size="small"
            label="Search loaded holds"
            value={filters.search}
            onChange={(e) => setFilters({ search: e.target.value })}
            placeholder="title, folder, file path"
            sx={{ minWidth: 240, flexGrow: 1 }}
            slotProps={{
              htmlInput: { 'aria-label': 'Search loaded holds' },
              input: {
                startAdornment: (
                  <InputAdornment position="start">
                    <SearchIcon fontSize="small" />
                  </InputAdornment>
                ),
                endAdornment: filters.search ? (
                  <InputAdornment position="end">
                    <IconButton
                      size="small"
                      aria-label="Clear search"
                      onClick={() => setFilters({ search: '' })}
                    >
                      <ClearIcon fontSize="small" />
                    </IconButton>
                  </InputAdornment>
                ) : null,
              },
            }}
            // The counterpart to the Kind helper text: this one never leaves the
            // browser, so an empty result says nothing about the queue.
            helperText="Matches the loaded page only"
          />

          <TextField
            select
            size="small"
            label="Sort"
            value={filters.sortBy}
            onChange={(e) => setFilters({ sortBy: e.target.value as RegroupSort })}
            sx={{ minWidth: 160 }}
            data-testid="regroup-sort-select"
            slotProps={{ select: { 'aria-label': 'Sort holds' } }}
            helperText="Orders holds and buckets"
          >
            {REGROUP_SORTS.map((s) => (
              <MenuItem key={s.value} value={s.value}>
                {s.label}
              </MenuItem>
            ))}
          </TextField>

          <Tooltip title="Reload the queue">
            <IconButton size="small" onClick={regroup.refresh} aria-label="Refresh review queue">
              <RefreshIcon fontSize="small" />
            </IconButton>
          </Tooltip>
        </Stack>

        <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: 'wrap', alignItems: 'center' }}>
          <Typography variant="body2" sx={{ color: 'text.secondary', flexGrow: 1 }}>
            Holds the system flagged for a human decision, grouped by type.
          </Typography>

          {/* The queue, all kinds — unchanged by any control in this rail. */}
          <Chip size="small" data-testid="regroup-total" label={`${regroup.queueTotal} pending`} />

          {filters.kind && (
            // The server's count for the selected kind. Named rather than left
            // to be inferred from the chip beside it, because the two are
            // different populations and reading one as the other is the whole
            // hazard of a kind-scoped `total`.
            <Chip
              size="small"
              color="primary"
              variant="outlined"
              data-testid="regroup-kind-total"
              label={`${regroup.total} in ${labelForKind(filters.kind)}`}
            />
          )}

          {regroup.loaded < regroup.total && (
            // Said out loud rather than left to be noticed. Bulk actions are
            // kind-scoped on the server, so they reach holds past this cut.
            <Tooltip
              title={`The queue is longer than one page: ${regroup.loaded} of ${regroup.total} matching holds were loaded. Bulk actions still apply to every pending hold of that kind, not just the ones loaded here. Narrowing by Kind spends the page on one kind.`}
            >
              <Chip
                size="small"
                color="warning"
                variant="outlined"
                data-testid="regroup-truncated"
                label={`${regroup.loaded} of ${regroup.total} loaded`}
              />
            </Tooltip>
          )}

          {searching && (
            // Deliberately NOT folded into the truncation chip above. This one
            // is the reviewer's own narrowing and carries no warning colour.
            <Chip
              size="small"
              variant="outlined"
              data-testid="regroup-search-count"
              label={`showing ${regroup.visible} of ${regroup.loaded} loaded`}
            />
          )}

          {regroup.filtersActive && (
            <Button size="small" onClick={regroup.clearFilters} data-testid="regroup-clear-filters">
              Clear filters
            </Button>
          )}
        </Stack>
      </Box>

      {/* A lane that is fetching must not look like a lane that has answered.
          The spine only spins when it has nothing at all, so a kind change with
          rows already on screen would otherwise be silent. */}
      {regroup.loading && <LinearProgress data-testid="regroup-loading" />}

      <Box sx={{ flex: 1, minHeight: 0, overflowY: 'auto' }}>
        {regroup.error && (
          <Stack sx={{ p: 2 }}>
            <Alert
              severity="error"
              data-testid="regroup-error"
              action={
                <Button color="inherit" size="small" onClick={regroup.refresh}>
                  Retry
                </Button>
              }
            >
              {regroup.error}
            </Alert>
          </Stack>
        )}
        <RegroupSpine lane={regroup} />
      </Box>
    </Box>
  );
}
