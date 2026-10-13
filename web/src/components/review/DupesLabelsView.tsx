// file: web/src/components/review/DupesLabelsView.tsx
// version: 1.1.0
// guid: 9a4e1c83-6d27-4b05-b3f8-2c7d0e5a6914
// last-edited: 2026-10-13
//
// The Duplicates lane's Labels and Suspicious sub-views: the Gold Labels page
// (pages/DedupLabels.tsx) rendered inside the lane. Rendering only -- every
// fetch and the override live in lanes/useDupesLabels.ts, and the caller owns
// the hook so the sub-view control and this view read one state.
//
// Every count on the stats strip is a button that shows the rows it counts: it
// replaces the filters with exactly that one and switches to the Labels
// sub-view if the Suspicious one was showing.

import { Fragment, useCallback, useMemo, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  FormControl,
  InputLabel,
  Link as MuiLink,
  MenuItem,
  Paper,
  Select,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import { useNavigate } from 'react-router-dom';
import { LabelToggle } from './compare/LabelToggle';
import { EvidencePanel } from './evidence/EvidencePanel';
import { dedupEvidence } from './evidence/adapters';
import { PathVarsLegend } from '../common/PathVarsLegend';
import {
  ColumnPicker,
  ResizableHeaderCell,
  type ColumnDef,
  useConfigurableTable,
} from '../common/ConfigurableTable';
import { formatPath, usePathVars, type PathVar } from '../../utils/formatPath';
import {
  LABELS_PAGE,
  type DupesLabels,
  type DupesSubView,
  type LabeledExample,
  type SuspiciousExample,
} from './lanes/useDupesLabels';

const labelColor = (l: string): 'success' | 'error' | 'warning' | 'default' =>
  l === 'true_dup' ? 'success' : l === 'not_dup' ? 'error' : l === 'unsure' ? 'warning' : 'default';

const sourceColor = (s: string): 'primary' | 'secondary' | 'info' | 'default' =>
  s === 'human'
    ? 'primary'
    : s === 'auto_high_conf'
      ? 'info'
      : s === 'rule'
        ? 'secondary'
        : 'default';

// BookCell renders one side of a labeled pair: a clickable title that opens the
// book, plus its abbreviated path (full path on hover).
function BookCell({
  bookId,
  title,
  path,
  pathVars,
  onOpen,
}: {
  bookId: string;
  title?: string;
  path?: string;
  pathVars: PathVar[];
  onOpen: (id: string) => void;
}) {
  return (
    <Box sx={{ minWidth: 0, maxWidth: 360 }}>
      <MuiLink
        component="button"
        type="button"
        underline="hover"
        onClick={() => onOpen(bookId)}
        sx={{ fontWeight: 600, textAlign: 'left', display: 'block', cursor: 'pointer' }}
        title={`Open "${title || bookId}"`}
      >
        {title || bookId}
      </MuiLink>
      {path && (
        <Tooltip
          title={path}
          placement="bottom-start"
          slotProps={{ tooltip: { sx: { maxWidth: 600 } } }}
        >
          <Typography
            variant="caption"
            noWrap
            sx={{
              color: 'text.secondary',
              fontFamily: 'monospace',
              fontSize: '0.65rem',
              display: 'block',
            }}
          >
            {formatPath(path, pathVars)}
          </Typography>
        </Tooltip>
      )}
    </Box>
  );
}

const evidenceId = (candidateId: number) => `dupes-label-evidence-${candidateId}`;
const whyText = (r: LabeledExample) => (r.score ? `${Math.round(r.score)} · Why` : 'Why');

// fmtDuration renders seconds as a compact h:mm label ("—" when unknown).
function fmtDuration(sec?: number): string {
  if (!sec || sec <= 0) return '—';
  const h = Math.floor(sec / 3600);
  const m = Math.round((sec % 3600) / 60);
  return `${h}h${m.toString().padStart(2, '0')}m`;
}

// SuspiciousQueue renders the rule-sourced not_dup labels that carry
// duplicate-shaped evidence, with per-row one-click overrides. Each button
// POSTs the EXISTING /dedup/labels/:id/override route (source→human) and, on
// success, the row is removed from the queue by the hook.
function SuspiciousQueue({
  rows,
  total,
  pending,
  status,
  error,
  pathVars,
  onOpen,
  onOverride,
  onRetry,
}: {
  rows: SuspiciousExample[];
  total: number;
  pending: ReadonlySet<number>;
  status: DupesLabels['suspiciousStatus'];
  error: string | null;
  pathVars: PathVar[];
  onOpen: (id: string) => void;
  onOverride: (candidateId: number, label: string) => void;
  onRetry: () => void;
}) {
  return (
    <Box>
      <Typography
        variant="body2"
        sx={{
          color: 'text.secondary',
          mb: 2,
        }}
      >
        Rule-sourced <code>not_dup</code> labels carrying duplicate-shaped evidence (shared
        ASIN/path, CERTAIN/HIGH band, cosine&nbsp;≥&nbsp;0.95, or the ms/sec duration-ratio
        signature). Each override becomes a permanent human gold label.
      </Typography>
      {status === 'populated' && (
        <Typography variant="caption" sx={{ color: 'text.secondary', display: 'block', mb: 1 }}>
          {rows.length} of {total} suspicious
        </Typography>
      )}
      {status === 'error' && (
        <Alert
          severity="error"
          sx={{ mb: 2 }}
          action={
            <Button color="inherit" size="small" onClick={onRetry}>
              Retry
            </Button>
          }
        >
          {error}
        </Alert>
      )}
      <TableContainer component={Paper}>
        <Table size="small" stickyHeader>
          <TableHead>
            <TableRow>
              <TableCell>Book A</TableCell>
              <TableCell>Book B</TableCell>
              <TableCell>Duration A / B</TableCell>
              <TableCell>Why suspicious</TableCell>
              <TableCell align="center">Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {status === 'loading' ? (
              <TableRow>
                <TableCell colSpan={5} align="center">
                  <CircularProgress
                    size={24}
                    sx={{ my: 2 }}
                    aria-label="Loading suspicious labels"
                  />
                </TableCell>
              </TableRow>
            ) : status === 'empty' ? (
              <TableRow>
                <TableCell colSpan={5} align="center">
                  <Typography
                    variant="body2"
                    sx={{
                      color: 'text.secondary',
                      py: 2,
                    }}
                  >
                    No suspicious labels in the queue.
                  </Typography>
                </TableCell>
              </TableRow>
            ) : (
              rows.map((r) => (
                <TableRow key={r.candidate_id} hover>
                  <TableCell>
                    <BookCell
                      bookId={r.entity_a_id}
                      title={r.a?.title}
                      path={r.a?.primary_path}
                      pathVars={pathVars}
                      onOpen={onOpen}
                    />
                  </TableCell>
                  <TableCell>
                    <BookCell
                      bookId={r.entity_b_id}
                      title={r.b?.title}
                      path={r.b?.primary_path}
                      pathVars={pathVars}
                      onOpen={onOpen}
                    />
                  </TableCell>
                  <TableCell>
                    <Typography variant="caption">
                      {fmtDuration(r.a?.total_duration_sec)} /{' '}
                      {fmtDuration(r.b?.total_duration_sec)}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <Stack direction="row" spacing={0.5} sx={{ flexWrap: 'wrap', gap: 0.5 }}>
                      {(r.suspicion_reasons || []).map((reason) => (
                        <Chip
                          key={reason}
                          size="small"
                          color="warning"
                          variant="outlined"
                          label={reason}
                        />
                      ))}
                    </Stack>
                  </TableCell>
                  <TableCell align="center">
                    <Stack
                      direction="row"
                      spacing={1}
                      sx={{
                        justifyContent: 'center',
                      }}
                    >
                      <Button
                        size="small"
                        color="success"
                        variant="outlined"
                        disabled={pending.has(r.candidate_id)}
                        onClick={() => onOverride(r.candidate_id, 'true_dup')}
                      >
                        Mark true_dup
                      </Button>
                      <Button
                        size="small"
                        color="error"
                        variant="outlined"
                        disabled={pending.has(r.candidate_id)}
                        onClick={() => onOverride(r.candidate_id, 'not_dup')}
                      >
                        Confirm not_dup
                      </Button>
                    </Stack>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </TableContainer>
    </Box>
  );
}

export interface DupesLabelsViewProps {
  /** Which of the two sub-views to show. */
  view: Exclude<DupesSubView, 'queue'>;
  labels: DupesLabels;
  /** Switches the sub-view; a stats count uses it to land on the Labels rows. */
  onSelectView: (view: DupesSubView) => void;
}

export function DupesLabelsView({ view, labels, onSelectView }: DupesLabelsViewProps) {
  const navigate = useNavigate();
  const pathVars = usePathVars();
  const openBook = useCallback((bookId: string) => navigate(`/library/${bookId}`), [navigate]);
  const [expandedId, setExpandedId] = useState<number | null>(null);
  const { override, pending } = labels;

  const columns = useMemo<ColumnDef<LabeledExample>[]>(
    () => [
      {
        key: 'book_a',
        label: 'Book A',
        defaultWidth: 360,
        sortable: false,
        render: (r) => (
          <BookCell
            bookId={r.entity_a_id}
            title={r.a?.title}
            path={r.a?.primary_path}
            pathVars={pathVars}
            onOpen={openBook}
          />
        ),
      },
      {
        key: 'book_b',
        label: 'Book B',
        defaultWidth: 360,
        sortable: false,
        render: (r) => (
          <BookCell
            bookId={r.entity_b_id}
            title={r.b?.title}
            path={r.b?.primary_path}
            pathVars={pathVars}
            onOpen={openBook}
          />
        ),
      },
      {
        key: 'layer',
        label: 'Layer',
        defaultWidth: 120,
        sortValue: (r) => r.layer,
        render: (r) => r.layer,
      },
      {
        key: 'band',
        label: 'Band',
        defaultWidth: 120,
        sortValue: (r) => r.band || '',
        render: (r) => r.band || '—',
      },
      {
        key: 'score',
        label: 'Score',
        defaultWidth: 130,
        sortValue: (r) => r.score ?? 0,
        render: (r) => (
          <Button
            size="small"
            aria-expanded={expandedId === r.candidate_id}
            aria-controls={expandedId === r.candidate_id ? evidenceId(r.candidate_id) : undefined}
            // The accessible name starts with the visible text (WCAG 2.5.3).
            aria-label={`${whyText(r)}, score breakdown for pair ${r.candidate_id}`}
            onClick={() => setExpandedId((cur) => (cur === r.candidate_id ? null : r.candidate_id))}
          >
            {whyText(r)}
          </Button>
        ),
      },
      {
        key: 'source',
        label: 'Source',
        defaultWidth: 150,
        sortValue: (r) => r.label_source,
        render: (r) => (
          <Chip
            size="small"
            variant="outlined"
            color={sourceColor(r.label_source)}
            label={r.label_source}
          />
        ),
      },
      {
        key: 'reason',
        label: 'Reason',
        defaultWidth: 220,
        sortValue: (r) => r.label_reason || '',
        render: (r) => (
          <Typography
            variant="caption"
            sx={{
              color: 'text.secondary',
            }}
          >
            {r.label_reason}
          </Typography>
        ),
      },
      {
        key: 'label',
        label: 'Label',
        align: 'center',
        defaultWidth: 220,
        sortValue: (r) => r.label,
        render: (r) => (
          <LabelToggle
            value={r.label}
            disabled={pending.has(r.candidate_id)}
            onChange={(label) => void override(r.candidate_id, label)}
          />
        ),
      },
    ],
    [openBook, pathVars, override, pending, expandedId]
  );

  const {
    visibleColumns,
    allColumns,
    sortField,
    sortDir,
    columnWidths,
    handleSort,
    toggleColumn,
    isColumnVisible,
    startResize,
    sortRows,
    resetColumns,
  } = useConfigurableTable<LabeledExample>({
    // Not 'dedup-labels': the old Gold Labels page saves its visible-column list
    // under that key, and a list saved without this view's Score column hides
    // the column here (and one saved here would hide the page's own columns).
    storageKey: 'review-dupes-labels',
    columns,
    defaultSortField: 'label',
  });

  const { stats, filters, offset, total } = labels;

  // Which count (if any) the table is currently showing the rows of: the
  // filters are exactly that one count's, and no band is narrowing them.
  const onLabels = view === 'labels';
  const noBand = !filters.band;
  const totalApplied = onLabels && noBand && !filters.label && !filters.source;
  const labelApplied = (k: string) => onLabels && noBand && filters.label === k && !filters.source;
  const sourceApplied = (k: string) => onLabels && noBand && filters.source === k && !filters.label;

  // A count shows the rows it counts: exactly that filter, on the Labels view.
  const showCount = (patch: Parameters<DupesLabels['showOnly']>[0]) => {
    labels.showOnly(patch);
    onSelectView('labels');
  };

  return (
    <Box sx={{ p: 3, overflow: 'auto', flex: 1, minHeight: 0 }} data-testid="dupes-labels-view">
      <Typography
        variant="body2"
        sx={{
          color: 'text.secondary',
          mb: 2,
        }}
      >
        Labeled duplicate-candidate examples the classifier trains and validates on. Human overrides
        become gold (<code>label_source=human</code>) and take precedence.
      </Typography>

      {stats.state === 'loading' && (
        <Typography variant="caption" sx={{ color: 'text.secondary' }} role="status">
          Loading label counts…
        </Typography>
      )}
      {stats.state === 'error' && (
        <Alert
          severity="warning"
          sx={{ mb: 2 }}
          action={
            <Button color="inherit" size="small" onClick={labels.reloadStats}>
              Retry counts
            </Button>
          }
        >
          Label counts unavailable: {stats.message}
        </Alert>
      )}
      {stats.state === 'ready' && (
        <Stack
          direction="row"
          spacing={1}
          sx={{ mb: 2, flexWrap: 'wrap', gap: 1 }}
          data-testid="dupes-labels-stats"
        >
          <Chip
            label={`total ${stats.value.total}`}
            variant={totalApplied ? 'filled' : 'outlined'}
            aria-pressed={totalApplied}
            onClick={() => showCount({})}
          />
          {Object.entries(stats.value.by_label).map(([k, v]) => (
            <Chip
              key={k}
              size="small"
              color={labelColor(k)}
              label={`${k}: ${v}`}
              variant={labelApplied(k) ? 'filled' : 'outlined'}
              aria-pressed={labelApplied(k)}
              onClick={() => showCount({ label: k })}
            />
          ))}
          {Object.entries(stats.value.by_source)
            .filter(([, v]) => v > 0)
            .map(([k, v]) => (
              <Chip
                key={k}
                size="small"
                color={sourceColor(k)}
                label={`${k}: ${v}`}
                variant={sourceApplied(k) ? 'filled' : 'outlined'}
                aria-pressed={sourceApplied(k)}
                onClick={() => showCount({ source: k })}
              />
            ))}
        </Stack>
      )}

      {labels.actionError && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {labels.actionError}
        </Alert>
      )}

      {view === 'labels' && (
        <>
          <Stack direction="row" spacing={2} sx={{ mb: 2 }}>
            <FormControl size="small" sx={{ minWidth: 160 }}>
              <InputLabel id="dedup-label-filter-label">Label</InputLabel>
              <Select
                id="dedup-label-filter"
                labelId="dedup-label-filter-label"
                label="Label"
                value={filters.label}
                onChange={(e) => labels.setLabelFilter(e.target.value)}
              >
                <MenuItem value="">All</MenuItem>
                <MenuItem value="true_dup">true_dup</MenuItem>
                <MenuItem value="not_dup">not_dup</MenuItem>
                <MenuItem value="unsure">unsure</MenuItem>
                <MenuItem value="unlabeled">unlabeled</MenuItem>
              </Select>
            </FormControl>
            <FormControl size="small" sx={{ minWidth: 180 }}>
              <InputLabel id="dedup-source-filter-label">Source</InputLabel>
              <Select
                id="dedup-source-filter"
                labelId="dedup-source-filter-label"
                label="Source"
                value={filters.source}
                onChange={(e) => labels.setSourceFilter(e.target.value)}
              >
                <MenuItem value="">All</MenuItem>
                <MenuItem value="human">human (gold)</MenuItem>
                <MenuItem value="auto_high_conf">auto_high_conf</MenuItem>
                <MenuItem value="rule">rule</MenuItem>
                <MenuItem value="itunes_attr">itunes_attr</MenuItem>
                <MenuItem value="llm_judge">llm_judge</MenuItem>
              </Select>
            </FormControl>
            <TextField
              size="small"
              label="Band"
              value={labels.bandInput}
              onChange={(e) => labels.setBandInput(e.target.value)}
              sx={{ minWidth: 180 }}
            />
            <Box sx={{ display: 'flex', alignItems: 'center', ml: 'auto' }}>
              <ColumnPicker
                columns={allColumns.map(({ key, label }) => ({ key, label }))}
                isVisible={isColumnVisible}
                onToggle={toggleColumn}
                onReset={resetColumns}
              />
            </Box>
          </Stack>

          {labels.status === 'error' && (
            <Alert
              severity="error"
              sx={{ mb: 2 }}
              action={
                <Button color="inherit" size="small" onClick={labels.reload}>
                  Retry
                </Button>
              }
            >
              {labels.error}
            </Alert>
          )}

          <TableContainer component={Paper}>
            <Table size="small" stickyHeader>
              <TableHead>
                <TableRow>
                  {visibleColumns.map((column) => (
                    <ResizableHeaderCell
                      key={column.key}
                      columnKey={column.key}
                      label={column.label}
                      width={columnWidths[column.key] ?? column.defaultWidth ?? 150}
                      align={column.align}
                      sortable={column.sortable !== false}
                      sortActive={sortField === column.key}
                      sortDirection={sortDir}
                      onSort={() => handleSort(column.key)}
                      onStartResize={startResize}
                    />
                  ))}
                </TableRow>
              </TableHead>
              <TableBody>
                {labels.status === 'loading' ? (
                  <TableRow>
                    <TableCell colSpan={visibleColumns.length} align="center">
                      <CircularProgress size={24} sx={{ my: 2 }} aria-label="Loading labels" />
                    </TableCell>
                  </TableRow>
                ) : labels.status === 'empty' ? (
                  <TableRow>
                    <TableCell colSpan={visibleColumns.length} align="center">
                      <Typography
                        variant="body2"
                        sx={{
                          color: 'text.secondary',
                          py: 2,
                        }}
                      >
                        No labeled examples for this filter.
                      </Typography>
                    </TableCell>
                  </TableRow>
                ) : labels.status === 'populated' ? (
                  sortRows(labels.rows).map((r) => (
                    <Fragment key={r.candidate_id}>
                      <TableRow hover>
                        {visibleColumns.map((column) => (
                          <TableCell
                            key={column.key}
                            align={column.align}
                            sx={{ width: columnWidths[column.key] ?? column.defaultWidth ?? 150 }}
                          >
                            {column.render(r)}
                          </TableCell>
                        ))}
                      </TableRow>
                      {expandedId === r.candidate_id && (
                        <TableRow>
                          <TableCell
                            colSpan={visibleColumns.length}
                            id={evidenceId(r.candidate_id)}
                          >
                            <EvidencePanel evidence={dedupEvidence(r.score_breakdown, r.layer)} />
                          </TableCell>
                        </TableRow>
                      )}
                    </Fragment>
                  ))
                ) : null}
              </TableBody>
            </Table>
          </TableContainer>

          <Stack
            direction="row"
            spacing={2}
            sx={{
              alignItems: 'center',
              mt: 2,
            }}
          >
            <Button
              disabled={offset === 0}
              onClick={() => labels.setOffset(Math.max(0, offset - LABELS_PAGE))}
            >
              Prev
            </Button>
            <Typography variant="body2">
              {total === 0 ? '0' : `${offset + 1}–${Math.min(offset + LABELS_PAGE, total)}`} of{' '}
              {total}
            </Typography>
            <Button
              disabled={offset + LABELS_PAGE >= total}
              onClick={() => labels.setOffset(offset + LABELS_PAGE)}
            >
              Next
            </Button>
          </Stack>
        </>
      )}

      {view === 'suspicious' && (
        <SuspiciousQueue
          rows={labels.suspiciousRows}
          total={labels.suspiciousTotal}
          pending={labels.pending}
          status={labels.suspiciousStatus}
          error={labels.suspiciousError}
          pathVars={pathVars}
          onOpen={openBook}
          onOverride={(id, label) => void labels.overrideSuspicious(id, label)}
          onRetry={labels.reloadSuspicious}
        />
      )}

      <PathVarsLegend />
    </Box>
  );
}
