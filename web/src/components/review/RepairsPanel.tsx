// file: web/src/components/review/RepairsPanel.tsx
// version: 1.2.0
// guid: 9c4f1a73-2e58-4b06-a9d1-6e3b8c7f0d52
// last-edited: 2026-09-29

/**
 * The repairs lane's surface: a rail of fixers and the selected fixer's trial.
 *
 * Every state the lane can be in renders differently, on purpose -- this
 * codebase has shipped pages where a failed request, an empty result and a hung
 * request looked the same:
 *
 *   fixer list loading / failed / empty (no fixers registered)
 *   no trial yet / trial running / trial failed
 *   rows loading / rows failed / zero rows for the tab / rows
 *   apply running / apply failed / apply result (with a preview called out)
 *
 * Applying during a library scan is allowed: the server pauses the scan briefly
 * while it writes. The copy says exactly that and never that anything is
 * blocked.
 */

import { useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';
import {
  Alert,
  AlertTitle,
  Box,
  Button,
  Checkbox,
  Chip,
  CircularProgress,
  LinearProgress,
  Link,
  List,
  ListItemButton,
  Stack,
  Tab,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TablePagination,
  TableRow,
  Tabs,
  Tooltip,
  Typography,
} from '@mui/material';
import RefreshIcon from '@mui/icons-material/Refresh';
import type { RepairFixer, RepairOpRef, RepairRow, RepairRowResult } from '../../services/api';
import {
  REPAIRS_PAGE_SIZES,
  summarizeApply,
  type RepairTrialState,
  type RepairsLane,
} from './lanes/useRepairsLane';

export interface RepairsPanelProps {
  repairs: RepairsLane;
}

/** "3 h ago" style age. Coarse on purpose: this is a freshness hint. */
export function ageOf(iso: string | undefined, now = Date.now()): string {
  if (!iso) return 'unknown time';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return 'unknown time';
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 60) return 'just now';
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.round(m / 60);
  if (h < 48) return `${h} h ago`;
  return `${Math.round(h / 24)} days ago`;
}

function sumCounts(m: Record<string, number> | undefined): number {
  return Object.values(m ?? {}).reduce((a, b) => a + b, 0);
}

function planSummary(lp: RepairOpRef | undefined, trial: RepairTrialState | undefined): string {
  if (trial?.phase === 'running') return 'Trial running…';
  if (trial?.phase === 'failed') return 'Last trial failed';
  if (!lp) return 'No trial yet';
  if (lp.status === 'completed') {
    return `Trial ${ageOf(lp.completed_at ?? lp.queued_at)} · ${lp.applicable ?? 0} applicable of ${lp.total ?? 0}`;
  }
  if (lp.status === 'queued' || lp.status === 'running') return 'Trial running…';
  return `Last trial ${lp.status}`;
}

function applySummary(la: RepairOpRef | undefined): string | null {
  if (!la) return null;
  if (!la.by_outcome) return `Last apply ${la.status} ${ageOf(la.completed_at ?? la.queued_at)}`;
  const o = la.by_outcome;
  const parts = [
    la.dry_run ? `would apply ${o.would_apply ?? 0}` : `applied ${o.applied ?? 0}`,
  ];
  if (o.changed_since_plan) parts.push(`changed ${o.changed_since_plan}`);
  if (o.failed) parts.push(`failed ${o.failed}`);
  return `Last apply ${ageOf(la.completed_at ?? la.queued_at)}${la.dry_run ? ' (preview)' : ''}: ${parts.join(', ')}`;
}

const OUTCOME_LABEL: Record<string, string> = {
  applied: 'Applied',
  would_apply: 'Would apply (preview)',
  changed_since_plan: 'Changed since trial',
  partially_applied: 'Partly applied',
  skipped_guard: 'Skipped by guard',
  not_applicable: 'Not applicable',
  failed: 'Failed',
  aborted_standdown_lost: 'Stopped: scan resumed',
};

const OUTCOME_COLOR: Record<
  string,
  'success' | 'info' | 'warning' | 'error' | 'default'
> = {
  applied: 'success',
  would_apply: 'info',
  changed_since_plan: 'warning',
  partially_applied: 'warning',
  skipped_guard: 'default',
  not_applicable: 'default',
  failed: 'error',
  aborted_standdown_lost: 'error',
};

function OutcomeChip({ result }: { result: RepairRowResult }) {
  const chip = (
    <Chip
      size="small"
      variant="outlined"
      color={OUTCOME_COLOR[result.outcome] ?? 'default'}
      label={OUTCOME_LABEL[result.outcome] ?? result.outcome}
      data-testid={`repairs-outcome-${result.row_id}`}
    />
  );
  const detail = result.error || result.skipped;
  return detail ? <Tooltip title={detail}>{chip}</Tooltip> : chip;
}

function DiffCell({ row }: { row: RepairRow }) {
  const cur = row.current ?? {};
  const prop = row.proposed ?? {};
  const keys = [...new Set([...Object.keys(cur), ...Object.keys(prop)])].sort();
  if (keys.length === 0) return <Typography variant="body2">—</Typography>;
  return (
    <Stack spacing={0.25}>
      {keys.map((k) => {
        const changed = cur[k] !== prop[k];
        return (
          <Typography
            key={k}
            variant="body2"
            sx={{ color: changed ? 'text.primary' : 'text.secondary', wordBreak: 'break-word' }}
          >
            <Box component="span" sx={{ fontWeight: 600 }}>
              {k}
            </Box>
            : {cur[k] ?? '—'}
            {changed && (
              <>
                {' → '}
                <Box component="span" sx={{ fontWeight: 600 }}>
                  {prop[k] ?? '—'}
                </Box>
              </>
            )}
          </Typography>
        );
      })}
    </Stack>
  );
}

function FixerRail({ repairs }: RepairsPanelProps) {
  const { fixers, selectedFixerId, trials } = repairs;
  return (
    <Box
      data-testid="repairs-rail"
      sx={{
        width: { xs: '100%', md: 320 },
        flexShrink: 0,
        borderRight: { md: 1 },
        borderBottom: { xs: 1, md: 0 },
        borderColor: 'divider',
        overflowY: 'auto',
      }}
    >
      <Stack direction="row" sx={{ alignItems: 'center', px: 2, pt: 1 }}>
        <Typography variant="overline" sx={{ flexGrow: 1 }}>
          Fixers
        </Typography>
        <Button
          size="small"
          startIcon={<RefreshIcon fontSize="small" />}
          onClick={repairs.reloadFixers}
          aria-label="Reload fixers"
        >
          Reload
        </Button>
      </Stack>
      {repairs.fixersLoading && <LinearProgress data-testid="repairs-fixers-loading" />}
      {repairs.fixersError && (
        <Alert
          severity="error"
          sx={{ m: 1 }}
          data-testid="repairs-fixers-error"
          action={
            <Button color="inherit" size="small" onClick={repairs.reloadFixers}>
              Retry
            </Button>
          }
        >
          {repairs.fixersError}
        </Alert>
      )}
      {!repairs.fixersLoading && !repairs.fixersError && fixers.length === 0 && (
        <Typography variant="body2" sx={{ p: 2, color: 'text.secondary' }} data-testid="repairs-no-fixers">
          No repair fixers are registered on this server.
        </Typography>
      )}
      <List dense disablePadding>
        {fixers.map((f: RepairFixer) => {
          const trial = trials[f.id];
          const running = trial?.phase === 'running';
          const apply = applySummary(f.last_apply);
          return (
            <ListItemButton
              key={f.id}
              selected={f.id === selectedFixerId}
              onClick={() => repairs.selectFixer(f.id)}
              data-testid={`repairs-fixer-${f.id}`}
              sx={{ display: 'block', py: 1, borderBottom: 1, borderColor: 'divider' }}
            >
              <Typography variant="subtitle2">{f.title}</Typography>
              <Typography variant="body2" sx={{ color: 'text.secondary', mb: 0.5 }}>
                {f.description}
              </Typography>
              <Typography variant="caption" component="div" data-testid={`repairs-fixer-plan-${f.id}`}>
                {planSummary(f.last_plan, trial)}
              </Typography>
              {apply && (
                <Typography variant="caption" component="div" sx={{ color: 'text.secondary' }}>
                  {apply}
                </Typography>
              )}
              <Button
                size="small"
                variant="outlined"
                sx={{ mt: 0.5 }}
                disabled={running}
                data-testid={`repairs-run-trial-${f.id}`}
                onClick={(e) => {
                  // The row is also a button (select); run the trial without
                  // re-selecting through the bubbling click.
                  e.stopPropagation();
                  repairs.selectFixer(f.id);
                  repairs.dispatch({ lane: 'repairs', type: 'runTrial', fixerId: f.id });
                }}
              >
                {running ? 'Trial running…' : 'Run trial'}
              </Button>
            </ListItemButton>
          );
        })}
      </List>
    </Box>
  );
}

/** Words for a fixer's row class, falling back to the class id. */
function classLabel(c: string): string {
  const labels: Record<string, string> = {
    moved: 'Moved',
    copy: 'Copy',
    'no-parent': 'No parent',
    'manual-only': 'Manual only',
    ambiguous: 'Ambiguous',
    held: 'Held',
  };
  return labels[c] ?? c;
}

/**
 * One chip per row class on the current tab, each with the count of rows it
 * lists. Clicking a chip narrows the rows to that class ("All" clears it), so
 * every count here opens exactly the rows, and through them the books, it
 * counts.
 */
function ClassChips({ repairs }: RepairsPanelProps) {
  const counts = repairs.page?.by_class_in_filter;
  if (!counts || Object.keys(counts).length === 0) {
    // Keep a stale class filter clearable even when the tab has no classes.
    if (!repairs.rowClass) return null;
  }
  const entries = Object.entries(counts ?? {}).sort(([a], [b]) => a.localeCompare(b));
  const total = entries.reduce((n, [, v]) => n + v, 0);
  return (
    <Stack
      direction="row"
      spacing={1}
      useFlexGap
      sx={{ px: 2, py: 1, flexWrap: 'wrap', alignItems: 'center' }}
      data-testid="repairs-class-chips"
    >
      <Chip
        size="small"
        label={`All (${total})`}
        color={repairs.rowClass === null ? 'primary' : 'default'}
        variant={repairs.rowClass === null ? 'filled' : 'outlined'}
        onClick={() => repairs.setRowClass(null)}
        data-testid="repairs-class-all"
      />
      {entries.map(([c, n]) => (
        <Chip
          key={c}
          size="small"
          label={`${classLabel(c)} (${n})`}
          color={repairs.rowClass === c ? 'primary' : 'default'}
          variant={repairs.rowClass === c ? 'filled' : 'outlined'}
          onClick={() => repairs.setRowClass(repairs.rowClass === c ? null : c)}
          data-testid={`repairs-class-${c}`}
        />
      ))}
    </Stack>
  );
}


/** Words for the skip kinds fixers report; an unknown kind shows as is. */
export const SKIP_KIND_LABEL: Record<string, string> = {
  fragment: 'Fragment — use the consolidation fixer',
  needs_manual: 'Needs manual',
  user_locked: 'User-locked title',
  skipped_itunes: 'iTunes library (hands-off)',
  skipped_owner_manual: 'Doctor Who / Big Finish / Torchwood (manual)',
  skipped_guard_unreadable: 'Could not read for the guard',
  error: 'Error',
};

/** True for the skipped tab and for any one-kind view of it. */
export function isSkippedFilter(filter: string): boolean {
  return filter === 'skipped' || filter.startsWith('skipped:');
}

/**
 * One chip per skip kind with its count. Each chip pages exactly the rows it
 * counts ("every count opens its books"); "All skipped" goes back.
 */
function SkipKindChips({ repairs }: RepairsPanelProps) {
  const counts = repairs.page?.skipped_by_kind ?? {};
  const kinds = Object.keys(counts).sort((a, b) => counts[b] - counts[a] || a.localeCompare(b));
  if (kinds.length === 0) return null;
  const total = sumCounts(counts);
  return (
    <Stack
      direction="row"
      spacing={1}
      useFlexGap
      sx={{ px: 2, py: 1, flexWrap: 'wrap' }}
      data-testid="repairs-skip-kinds"
    >
      <Chip
        size="small"
        clickable
        label={`All skipped (${total})`}
        color={repairs.filter === 'skipped' ? 'primary' : 'default'}
        variant={repairs.filter === 'skipped' ? 'filled' : 'outlined'}
        onClick={() => repairs.setFilter('skipped')}
        data-testid="repairs-skip-kind-all"
      />
      {kinds.map((k) => {
        const active = repairs.filter === `skipped:${k}`;
        return (
          <Chip
            key={k}
            size="small"
            clickable
            label={`${SKIP_KIND_LABEL[k] ?? k} (${counts[k]})`}
            color={active ? 'primary' : 'default'}
            variant={active ? 'filled' : 'outlined'}
            onClick={() => repairs.setFilter(`skipped:${k}`)}
            data-testid={`repairs-skip-kind-${k}`}
          />
        );
      })}
    </Stack>
  );
}


/**
 * Every book of a row, each a link to the book, with its role and file
 * counts. Rows with one book show nothing extra (the title above links it);
 * rows with more show a "N books" toggle that lists them all.
 */
function RowMembers({ row }: { row: RepairRow }) {
  const [open, setOpen] = useState(false);
  const members =
    row.members && row.members.length > 0
      ? row.members
      : row.book_ids.map((id) => ({ book_id: id, files: 0 }) as NonNullable<RepairRow['members']>[number]);
  if (members.length <= 1) return null;
  return (
    <Box>
      <Button
        size="small"
        variant="text"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        sx={{ p: 0, minWidth: 0, textTransform: 'none' }}
        data-testid={`repairs-row-members-${row.row_id}`}
      >
        {members.length} books in this row
      </Button>
      {open && (
        <Box component="ul" sx={{ m: 0, pl: 2 }}>
          {members.map((m) => (
            <li key={m.book_id}>
              <Link component={RouterLink} to={`/library/${encodeURIComponent(m.book_id)}`}>
                {m.title || m.book_id}
              </Link>
              <Typography variant="caption" sx={{ color: 'text.secondary', ml: 0.5 }}>
                {[m.role, m.files ? `${m.files} file${m.files === 1 ? '' : 's'}` : '', m.missing_files ? `${m.missing_files} missing` : '']
                  .filter(Boolean)
                  .join(' · ')}
              </Typography>
            </li>
          ))}
        </Box>
      )}
    </Box>
  );
}

const EVIDENCE_SHOWN = 3;

/** What the row's decision was made from; long lists fold behind a toggle. */
function RowEvidence({ row }: { row: RepairRow }) {
  const [all, setAll] = useState(false);
  const ev = row.evidence ?? [];
  if (ev.length === 0) return null;
  const shown = all ? ev : ev.slice(0, EVIDENCE_SHOWN);
  return (
    <Box data-testid={`repairs-row-evidence-${row.row_id}`}>
      {shown.map((e, i) => (
        <Typography key={i} variant="caption" component="div" sx={{ color: 'text.secondary', wordBreak: 'break-all' }}>
          {e}
        </Typography>
      ))}
      {ev.length > EVIDENCE_SHOWN && (
        <Button size="small" onClick={() => setAll((v) => !v)} sx={{ p: 0, minWidth: 0, textTransform: 'none' }}>
          {all ? 'Show less' : `Show all ${ev.length}`}
        </Button>
      )}
    </Box>
  );
}

function RowsTable({ repairs }: RepairsPanelProps) {
  const { rows, filter, selectedRowIds, rowOutcomes, settledRowIds } = repairs;
  const skippedTab = isSkippedFilter(filter);
  return (
    <Table size="small" stickyHeader data-testid="repairs-rows">
      <TableHead>
        <TableRow>
          {!skippedTab && <TableCell padding="checkbox" />}
          <TableCell>Book</TableCell>
          <TableCell>Current → proposed</TableCell>
          <TableCell>{skippedTab ? 'Why skipped' : 'Reason'}</TableCell>
          <TableCell>Risk</TableCell>
          {!skippedTab && <TableCell>Result</TableCell>}
        </TableRow>
      </TableHead>
      <TableBody>
        {rows.map((row) => {
          const outcome = rowOutcomes.get(row.row_id);
          const bookId = row.book_ids[0];
          return (
            <TableRow key={row.row_id} hover data-testid={`repairs-row-${row.row_id}`}>
              {!skippedTab && (
                <TableCell padding="checkbox">
                  {/* Skipped rows never reach this tab's checkbox column; the
                      guard is here too so a mixed page cannot offer one. A row
                      an apply already settled is not offered again either:
                      the stored trial still lists it, the library no longer
                      matches it. */}
                  {!row.skipped && !settledRowIds.has(row.row_id) && (
                    <Checkbox
                      size="small"
                      checked={selectedRowIds.has(row.row_id)}
                      onChange={() => repairs.toggleRow(row.row_id)}
                      slotProps={{ input: { 'aria-label': `Select ${row.title || row.row_id}` } }}
                    />
                  )}
                </TableCell>
              )}
              <TableCell sx={{ minWidth: 180 }}>
                {bookId ? (
                  <Link component={RouterLink} to={`/library/${encodeURIComponent(bookId)}`}>
                    {row.title || bookId}
                  </Link>
                ) : (
                  row.title || row.row_id
                )}
                {row.author && (
                  <Typography variant="body2" sx={{ color: 'text.secondary' }}>
                    {row.author}
                  </Typography>
                )}
                {row.class && (
                  <Box>
                    <Chip size="small" variant="outlined" label={classLabel(row.class)} sx={{ mt: 0.5 }} />
                  </Box>
                )}
                <RowMembers row={row} />
              </TableCell>
              <TableCell sx={{ minWidth: 220 }}>
                <DiffCell row={row} />
              </TableCell>
              <TableCell sx={{ minWidth: 200 }}>
                {skippedTab ? (
                  <>
                    <Typography variant="body2">{row.skip_reason || row.skipped}</Typography>
                    {row.skip_reason && row.skipped && (
                      <Typography variant="caption" sx={{ color: 'text.secondary' }}>
                        {row.skipped}
                      </Typography>
                    )}
                  </>
                ) : (
                  <Typography variant="body2">{row.reason}</Typography>
                )}
                <RowEvidence row={row} />
              </TableCell>
              <TableCell>
                <Chip
                  size="small"
                  label={row.risk === 'review' ? 'Review' : row.risk === 'low' ? 'Low' : row.risk}
                  color={row.risk === 'review' ? 'warning' : 'default'}
                  variant={row.risk === 'low' ? 'outlined' : 'filled'}
                />
              </TableCell>
              {!skippedTab && <TableCell>{outcome && <OutcomeChip result={outcome} />}</TableCell>}
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}

function PlanView({ repairs }: RepairsPanelProps) {
  const fixer = repairs.selectedFixer;
  const { page, planOpId, trial } = repairs;
  if (!fixer) return null;

  const runTrial = () => repairs.dispatch({ lane: 'repairs', type: 'runTrial', fixerId: fixer.id });
  const running = trial?.phase === 'running';
  const skippedTotal = sumCounts(page?.skipped_by_kind);
  const selectedCount = repairs.selectedRowIds.size;
  const applicable = page?.applicable ?? 0;
  const remaining = repairs.remainingApplicable ?? 0;
  const applyDisabled = repairs.applying || running;

  return (
    <Box sx={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
      <Box sx={{ px: 2, py: 1.5, borderBottom: 1, borderColor: 'divider' }}>
        <Typography variant="h6">{fixer.title}</Typography>
        <Typography variant="body2" sx={{ color: 'text.secondary' }}>
          {fixer.description}
        </Typography>
        <Typography variant="caption" component="div" sx={{ color: 'text.secondary', mt: 0.5 }}>
          A trial writes nothing. Applying writes the chosen rows and records each change in the
          book history. If a library scan is running, it pauses briefly while the rows are written.
        </Typography>
      </Box>

      {running && (
        <Alert severity="info" sx={{ m: 2, mb: 0 }} data-testid="repairs-trial-running" icon={<CircularProgress size={18} />}>
          Trial running. Rows appear here when it finishes
          {planOpId ? '; the rows below are from the previous trial.' : '.'}
        </Alert>
      )}
      {trial?.phase === 'failed' && (
        <Alert
          severity="error"
          sx={{ m: 2, mb: 0 }}
          data-testid="repairs-trial-failed"
          action={
            <Button color="inherit" size="small" onClick={runTrial}>
              Run trial again
            </Button>
          }
        >
          {trial.message}
        </Alert>
      )}
      {!trial &&
        fixer.last_plan &&
        fixer.last_plan.status !== 'completed' &&
        fixer.last_plan.status !== 'queued' &&
        fixer.last_plan.status !== 'running' && (
        <Alert severity="error" sx={{ m: 2, mb: 0 }} data-testid="repairs-last-trial-failed">
          The last trial ended {fixer.last_plan.status}
          {fixer.last_plan.error ? `: ${fixer.last_plan.error}` : '.'}
        </Alert>
      )}

      {!planOpId && !running && (
        <Box sx={{ p: 3 }} data-testid="repairs-no-trial">
          <Typography variant="body2" sx={{ mb: 1 }}>
            No completed trial for this fixer yet. Run one to see what it would change.
          </Typography>
          <Button variant="contained" onClick={runTrial}>
            Run trial
          </Button>
        </Box>
      )}

      {planOpId && (
        <>
          <Stack
            direction="row"
            spacing={1}
            useFlexGap
            sx={{ px: 2, pt: 1, flexWrap: 'wrap', alignItems: 'center' }}
          >
            <Tabs
              value={isSkippedFilter(repairs.filter) ? 'skipped' : 'applicable'}
              onChange={(_, v: 'applicable' | 'skipped') => repairs.setFilter(v)}
              aria-label="Trial rows"
              sx={{ minHeight: 40 }}
            >
              <Tab
                value="applicable"
                label={page ? `Applicable (${applicable})` : 'Applicable'}
                data-testid="repairs-tab-applicable"
              />
              <Tab
                value="skipped"
                label={page ? `Skipped (${skippedTotal})` : 'Skipped'}
                data-testid="repairs-tab-skipped"
              />
            </Tabs>
            {page && (
              <Chip size="small" variant="outlined" label={`Trial ${ageOf(page.planned_at)}`} />
            )}
            <Box sx={{ flexGrow: 1 }} />
            <Button size="small" onClick={runTrial} disabled={running} data-testid="repairs-rerun-trial">
              Re-run trial
            </Button>
          </Stack>
          <ClassChips repairs={repairs} />

          {repairs.filter === 'applicable' && (
            <Stack
              direction="row"
              spacing={1}
              useFlexGap
              sx={{ px: 2, py: 1, flexWrap: 'wrap', alignItems: 'center' }}
            >
              <Button size="small" onClick={repairs.selectPage} disabled={repairs.rows.length === 0}>
                Select page
              </Button>
              <Button size="small" onClick={repairs.clearSelection} disabled={selectedCount === 0}>
                Clear
              </Button>
              <Typography variant="body2" data-testid="repairs-selected-count">
                {selectedCount} selected
              </Typography>
              <Box sx={{ flexGrow: 1 }} />
              <Button
                size="small"
                variant="contained"
                disabled={applyDisabled || selectedCount === 0}
                data-testid="repairs-apply-selected"
                onClick={() =>
                  repairs.dispatch({
                    lane: 'repairs',
                    type: 'applyRows',
                    fixerId: fixer.id,
                    planOpId,
                    rowIds: [...repairs.selectedRowIds],
                  })
                }
              >
                Apply selected ({selectedCount})
              </Button>
              <Button
                size="small"
                variant="outlined"
                color="warning"
                disabled={applyDisabled || !page || remaining === 0}
                data-testid="repairs-apply-all"
                onClick={() =>
                  repairs.dispatch({
                    lane: 'repairs',
                    type: 'applyAllApplicable',
                    fixerId: fixer.id,
                    planOpId,
                  })
                }
              >
                Apply all applicable ({remaining})
              </Button>
            </Stack>
          )}

          {isSkippedFilter(repairs.filter) && <SkipKindChips repairs={repairs} />}

          {repairs.applying && (
            <Alert severity="info" sx={{ mx: 2 }} data-testid="repairs-applying" icon={<CircularProgress size={18} />}>
              Applying… If a library scan is running it pauses briefly while the rows are written.
            </Alert>
          )}
          {repairs.applyError && (
            <Alert
              severity={repairs.applyError.severity}
              sx={{ mx: 2 }}
              data-testid="repairs-apply-error"
            >
              {repairs.applyError.message}
            </Alert>
          )}
          {repairs.applyResult && (
            <Alert
              severity={
                repairs.applyResult.failed > 0 ||
                repairs.applyResult.partially_applied > 0 ||
                repairs.applyResult.aborted ||
                repairs.applyResult.dry_run
                  ? 'warning'
                  : 'success'
              }
              sx={{ mx: 2 }}
              data-testid="repairs-apply-result"
              action={
                <Button color="inherit" size="small" onClick={runTrial} disabled={running}>
                  Re-run trial
                </Button>
              }
            >
              <AlertTitle>{repairs.applyResult.dry_run ? 'Preview' : 'Apply finished'}</AlertTitle>
              {summarizeApply(repairs.applyResult)}
              {repairs.applyResult.changed_since_plan > 0 &&
                '. Rows that changed since the trial were left alone; re-run the trial to pick them up.'}
            </Alert>
          )}

          {repairs.rowsLoading && <LinearProgress data-testid="repairs-rows-loading" />}
          <Box sx={{ flex: 1, minHeight: 0, overflow: 'auto' }}>
            {repairs.rowsError && (
              <Alert
                severity="error"
                sx={{ m: 2 }}
                data-testid="repairs-rows-error"
                action={
                  <Button color="inherit" size="small" onClick={repairs.reloadRows}>
                    Retry
                  </Button>
                }
              >
                {repairs.rowsError}
              </Alert>
            )}
            {page && page.total === 0 && (
              <Typography variant="body2" sx={{ p: 3, color: 'text.secondary' }} data-testid="repairs-rows-empty">
                {repairs.filter === 'applicable'
                  ? 'This trial found nothing to repair.'
                  : 'The trial skipped no rows.'}
              </Typography>
            )}
            {page && page.total > 0 && <RowsTable repairs={repairs} />}
          </Box>
          {page && page.total > 0 && (
            <TablePagination
              component="div"
              count={page.total}
              page={Math.floor(repairs.offset / repairs.pageSize)}
              rowsPerPage={repairs.pageSize}
              rowsPerPageOptions={[...REPAIRS_PAGE_SIZES]}
              onPageChange={(_, p) => repairs.setOffset(p * repairs.pageSize)}
              onRowsPerPageChange={(e) => repairs.setPageSize(Number(e.target.value))}
            />
          )}
        </>
      )}
    </Box>
  );
}

export function RepairsPanel({ repairs }: RepairsPanelProps) {
  return (
    <Box
      data-testid="repairs-panel"
      sx={{
        flex: 1,
        minHeight: 0,
        display: 'flex',
        flexDirection: { xs: 'column', md: 'row' },
      }}
    >
      <FixerRail repairs={repairs} />
      {repairs.selectedFixer ? (
        <PlanView repairs={repairs} />
      ) : (
        !repairs.fixersLoading &&
        !repairs.fixersError &&
        repairs.fixers.length > 0 && (
          <Typography variant="body2" sx={{ p: 3, color: 'text.secondary' }}>
            Pick a fixer on the left.
          </Typography>
        )
      )}
    </Box>
  );
}
