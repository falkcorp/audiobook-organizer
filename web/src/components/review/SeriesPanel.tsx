// file: web/src/components/review/SeriesPanel.tsx
// version: 1.0.0
// guid: e7b3a915-2c64-4d08-b1f7-90a5c8d3e426
// last-edited: 2026-10-13

/**
 * The Authors & series lane's Series sub-view: a rail, the series group spine,
 * and the sub-view's action bar.
 *
 * THE FOUR STATES (plus one). Loading, error, empty and populated each render
 * differently, and so does a cold cache (`needs-scan`): the server answered, but
 * nobody has scanned, which is neither "no duplicates" nor a failure. The old tab
 * ran the scan for you when it saw a cold cache; this one says so and offers the
 * button, because a scan is a job that costs something and the reviewer should
 * choose to start it.
 *
 * COUNTS CLICK THROUGH. The kind chips (All / Same name / Sub-series) partition
 * the list and each filters the spine to exactly the rows its number counts.
 * "Merge Selected (N)" counts the ticked groups that still exist and have two
 * series in. "Merge All (N)" counts the same-name groups Merge All merges -- the
 * chip named "Same name" is the list of them -- and the confirm dialog lists
 * every one by name before anything is sent. The "series in the library" total
 * links to the Series page.
 */

import { useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  IconButton,
  Link,
  LinearProgress,
  Paper,
  Stack,
  TablePagination,
  Tooltip,
  Typography,
} from '@mui/material';
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import MergeIcon from '@mui/icons-material/MergeType';
import RefreshIcon from '@mui/icons-material/Refresh';
import { BulkConfirmDialog } from '../common/BulkConfirmDialog';
import { SelectAllMatchingBanner } from '../common/SelectAllMatchingBanner';
import { AuthorsSubViewSwitch } from './AuthorsSubViewSwitch';
import { SERIES_PAGE_SIZES, SERIES_SHORTCUTS, type AuthorsLane } from './lanes/useAuthorsLane';
import type { SeriesKindFilter } from './lanes/seriesGroups';
import { SeriesGroupSpine } from './spine/SeriesGroupSpine';

const KIND_CHIPS: ReadonlyArray<{ value: SeriesKindFilter; label: string }> = [
  { value: 'all', label: 'All groups' },
  { value: 'exact', label: 'Same name' },
  { value: 'subseries', label: 'Sub-series' },
];

export function SeriesPanel({ authors }: { authors: AuthorsLane }) {
  const series = authors.series;
  const { selection } = series;
  // The groups the reviewer is confirming, captured when the dialog opened: the
  // names to list, and the keys the lane compares against before it sends.
  const [confirmAll, setConfirmAll] = useState<Array<{ key: string; name: string }> | null>(null);
  const [confirmSelected, setConfirmSelected] = useState(false);
  const mergeAllCount = series.mergeAll.length;
  const bulkHere = authors.bulk?.entity === 'series' ? authors.bulk : null;

  const mergeSelected = () => {
    setConfirmSelected(false);
    authors.dispatch({
      lane: 'authors',
      entity: 'series',
      type: 'mergeSelectedSeries',
      groupKeys: series.selectedGroups.map((g) => g.key),
    });
  };
  const requestMergeSelected = () => {
    // A selection wider than the page asks first, with its count.
    if (series.selectedCount > series.pageGroups.length) setConfirmSelected(true);
    else mergeSelected();
  };

  const totalLink = (
    <Link component={RouterLink} to="/series" data-testid="series-total-link">
      {series.totalSeries} series in the library
    </Link>
  );

  return (
    <Box sx={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' }} data-testid="series-panel">
      <Box
        data-testid="series-rail"
        sx={{ borderBottom: 1, borderColor: 'divider', px: 2, py: 1, display: 'flex', flexDirection: 'column', gap: 1 }}
      >
        <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: 'wrap', alignItems: 'center' }}>
          <AuthorsSubViewSwitch view={authors.view} onChange={authors.setView} />
          <Typography variant="body2" sx={{ color: 'text.secondary', flexGrow: 1 }}>
            Series with identical names (ignoring case), often made by reimports that created a series with and
            without an author link. {series.status === 'populated' ? totalLink : null}
          </Typography>
          {series.groups.length > 0 && (
            <>
              {series.selectedCount > 0 && (
                <Button
                  variant="contained"
                  startIcon={<MergeIcon />}
                  onClick={requestMergeSelected}
                  disabled={authors.busy}
                >
                  Merge Selected ({series.selectedCount})
                </Button>
              )}
              <Button
                variant="contained"
                color="warning"
                startIcon={<MergeIcon />}
                onClick={() => setConfirmAll(series.mergeAll.map((g) => ({ key: g.key, name: g.name })))}
                disabled={authors.busy || mergeAllCount === 0}
              >
                Merge All ({mergeAllCount})
              </Button>
            </>
          )}
          <Button size="small" onClick={() => series.setHelpOpen(true)}>
            Keyboard shortcuts (?)
          </Button>
          <Tooltip title="Refresh">
            <span>
              <IconButton onClick={series.refresh} disabled={authors.busy} aria-label="Refresh">
                <RefreshIcon />
              </IconButton>
            </span>
          </Tooltip>
        </Stack>

        {series.groups.length > 0 && (
          <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: 'wrap', alignItems: 'center' }}>
            {KIND_CHIPS.map(({ value, label }) => (
              <Chip
                key={value}
                size="small"
                color={series.filter === value ? 'primary' : 'default'}
                variant={series.filter === value ? 'filled' : 'outlined'}
                label={`${label} (${series.kindCounts[value]})`}
                aria-pressed={series.filter === value}
                data-testid={`series-kind-${value}`}
                onClick={() => series.setFilter(value)}
              />
            ))}
          </Stack>
        )}
      </Box>

      {series.stale && (
        <Alert
          severity="warning"
          data-testid="series-stale"
          sx={{ mx: 2, my: 1 }}
          action={
            <Button color="inherit" size="small" onClick={series.refresh} disabled={authors.busy}>
              Rescan
            </Button>
          }
        >
          These results are stale since your last change: the server cleared its list of duplicate series, so what
          is shown is the list as this screen edited it. Rescan to rebuild it from the library.
        </Alert>
      )}

      {bulkHere && (
        <Box sx={{ px: 2 }} data-testid="series-bulk-progress">
          <LinearProgress variant="determinate" value={(bulkHere.done / bulkHere.total) * 100} />
          <Typography variant="caption" sx={{ color: 'text.secondary' }}>
            Processed {bulkHere.done} of {bulkHere.total} groups
          </Typography>
        </Box>
      )}
      {authors.scanning && <LinearProgress data-testid="series-scanning" />}

      <Box sx={{ flex: 1, minHeight: 0, overflowY: 'auto', px: 2, py: 1 }}>
        {series.status === 'loading' && (
          <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }} data-testid="series-loading">
            <CircularProgress />
          </Box>
        )}

        {series.status === 'error' && (
          <Alert
            severity="error"
            data-testid="series-error"
            action={
              <Button color="inherit" size="small" onClick={series.reload}>
                Retry
              </Button>
            }
          >
            Could not load duplicate series: {series.error}
          </Alert>
        )}

        {series.status === 'needs-scan' && (
          <Paper sx={{ p: 4, textAlign: 'center' }} data-testid="series-needs-scan">
            <Typography variant="h6">Series have not been scanned yet</Typography>
            <Typography variant="body2" sx={{ color: 'text.secondary', mb: 2 }}>
              The server holds no duplicate-series results. This is not &quot;no duplicates&quot; — run the scan to
              find out.
            </Typography>
            <Button variant="contained" onClick={series.refresh} disabled={authors.scanning}>
              Scan for duplicate series
            </Button>
          </Paper>
        )}

        {series.status === 'empty' && (
          <Paper sx={{ p: 4, textAlign: 'center' }} data-testid="series-empty">
            <CheckCircleIcon sx={{ fontSize: 48, color: 'success.main', mb: 1 }} />
            <Typography variant="h6">No duplicate series found</Typography>
            <Typography variant="body2" sx={{ color: 'text.secondary' }}>
              {totalLink}, none of them sharing a name.
            </Typography>
          </Paper>
        )}

        {series.status === 'populated' && series.filtered.length === 0 && (
          <Paper sx={{ p: 3, textAlign: 'center' }} data-testid="series-filter-empty">
            <Typography variant="body1">No groups of this kind.</Typography>
            <Button size="small" onClick={() => series.setFilter('all')}>
              Show all groups
            </Button>
          </Paper>
        )}

        {series.status === 'populated' && series.filtered.length > 0 && (
          <>
            <Stack direction="row" spacing={1} sx={{ alignItems: 'center', mb: 1 }}>
              <Checkbox
                size="small"
                checked={selection.header.checked}
                indeterminate={selection.header.indeterminate}
                disabled={authors.busy || selection.header.disabled}
                onChange={selection.togglePage}
                slotProps={{
                  input: { 'aria-label': `Select all ${series.pageGroups.length} groups on this page` },
                }}
              />
              <Typography variant="caption" color="text.secondary">
                Select page · Shift-click a checkbox to select a range
              </Typography>
            </Stack>
            <SelectAllMatchingBanner
              selection={selection}
              pageCount={series.pageGroups.length}
              totalMatching={series.selectableTotal}
              noun="groups"
              testIdPrefix="series-groups-select-all"
            />
            <SeriesGroupSpine lane={authors} />
            <TablePagination
              component="div"
              count={series.filtered.length}
              page={series.page}
              onPageChange={(_, p) => series.setPage(p)}
              rowsPerPage={series.pageSize}
              onRowsPerPageChange={(e) => series.setPageSize(parseInt(e.target.value, 10))}
              rowsPerPageOptions={[...SERIES_PAGE_SIZES]}
              labelRowsPerPage="Groups per page:"
              sx={{ mt: 2 }}
            />
          </>
        )}
      </Box>

      <Dialog open={confirmAll !== null} onClose={() => setConfirmAll(null)} data-testid="series-merge-all-confirm">
        <DialogTitle>Confirm Merge All</DialogTitle>
        <DialogContent>
          <DialogContentText component="div">
            This will merge {confirmAll?.length} same-name series group{confirmAll?.length === 1 ? '' : 's'}, one
            after another. In each, the series that are ticked are merged into the first one, and your left-out
            series and narrator flags are honoured. Sub-series groups are not included: merge those one at a time.
            This action cannot be undone from this screen. Are you sure?
          </DialogContentText>
          <DialogContentText sx={{ mt: 1 }} data-testid="series-merge-all-warning">
            Keep this lane open: leaving it stops the remaining merges.
          </DialogContentText>
          <Box
            component="ul"
            data-testid="series-merge-all-list"
            sx={{ maxHeight: 220, overflowY: 'auto', mt: 1, mb: 0, pl: 3 }}
          >
            {confirmAll?.map((g) => (
              <li key={g.key}>
                <Typography variant="body2">{g.name}</Typography>
              </li>
            ))}
          </Box>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmAll(null)}>Cancel</Button>
          <Button
            color="warning"
            variant="contained"
            onClick={() => {
              const expectedKeys = (confirmAll ?? []).map((g) => g.key);
              setConfirmAll(null);
              authors.dispatch({ lane: 'authors', entity: 'series', type: 'mergeAllSeriesGroups', expectedKeys });
            }}
          >
            Confirm
          </Button>
        </DialogActions>
      </Dialog>

      <BulkConfirmDialog
        open={confirmSelected}
        title={`Merge ${series.selectedCount} selected series groups?`}
        confirmLabel={`Merge ${series.selectedCount}`}
        onCancel={() => setConfirmSelected(false)}
        onConfirm={mergeSelected}
        testId="series-merge-selected-confirm"
      >
        The selection covers {series.selectedCount} groups, more than the {series.pageGroups.length} on this page.
        In each group, the ticked series are merged into the first one.
      </BulkConfirmDialog>

      <Dialog open={series.helpOpen} onClose={() => series.setHelpOpen(false)} data-testid="series-shortcut-help">
        <DialogTitle>Keyboard shortcuts</DialogTitle>
        <DialogContent>
          <Stack spacing={1} sx={{ minWidth: 280 }}>
            {SERIES_SHORTCUTS.map((s) => (
              <Stack key={s.keys} direction="row" spacing={2} sx={{ justifyContent: 'space-between' }}>
                <Chip label={s.keys} size="small" variant="outlined" />
                <Typography variant="body2">{s.action}</Typography>
              </Stack>
            ))}
          </Stack>
        </DialogContent>
      </Dialog>
    </Box>
  );
}
