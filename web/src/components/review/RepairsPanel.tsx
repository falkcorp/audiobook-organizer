// file: web/src/components/review/RepairsPanel.tsx
// version: 1.24.0
// guid: 9c4f1a73-2e58-4b06-a9d1-6e3b8c7f0d52
// last-edited: 2026-10-08

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
 * Owner rows (`owner_applicable`) are skipped rows only the owner applies,
 * one at a time, from their own "Apply (owner)" button: they never get a
 * checkbox, "Select page" or "Apply all" never reach them, and the server
 * honours the request only for the owner's interactive sign-in.
 *
 * Applying during a library scan is allowed: the server pauses the scan briefly
 * while it writes. The copy says exactly that and never that anything is
 * blocked.
 *
 * The rail is resizable (drag the divider, or focus it and use the arrow keys)
 * and its width is remembered per browser. Fixer descriptions are clamped in
 * the rail and collapsed in the header behind "More"; the expanded state is
 * remembered too. Both are per-viewer conveniences, so storage failures fall
 * back to the defaults.
 *
 * The rows render in the workspace's three view modes (2026-10-08): compact
 * is one line per row that opens for detail (repairs/RepairsCompactView),
 * two-column is a card per row with book metadata and files
 * (repairs/RepairsDetailsView), and candidates groups the page's rows by fix
 * (repairs/RepairsGroupedView).
 */

import { useCallback, useRef, useState } from 'react';
import {
  Alert,
  AlertTitle,
  Box,
  Button,
  Chip,
  CircularProgress,
  LinearProgress,
  List,
  ListItemButton,
  Stack,
  Tab,
  TablePagination,
  Tabs,
  Typography,
} from '@mui/material';
import RefreshIcon from '@mui/icons-material/Refresh';
import type { RepairFixer, RepairOpRef } from '../../services/api';
import {
  REPAIRS_PAGE_SIZES,
  applySeverity,
  summarizeApply,
  type RepairTrialState,
  type RepairsLane,
} from './lanes/useRepairsLane';
import type { SpineViewMode } from './spine/viewMode';
import { classLabel, isSkippedFilter } from './repairs/rowHelpers';
import { RepairsCompactView } from './repairs/RepairsCompactView';
import { RepairsDetailsView } from './repairs/RepairsDetailsView';
import { RepairsGroupedView } from './repairs/RepairsGroupedView';

export interface RepairsPanelProps {
  repairs: RepairsLane;
  /**
   * The workspace's view toggle: compact = Compact rows, two-column =
   * Details, candidates = Grouped by fix.
   */
  viewMode?: SpineViewMode;
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
  if (o.retry_later) parts.push(`retry later ${o.retry_later}`);
  return `Last apply ${ageOf(la.completed_at ?? la.queued_at)}${la.dry_run ? ' (preview)' : ''}: ${parts.join(', ')}`;
}

const RAIL_WIDTH_KEY = 'repairs.railWidth';
const DESC_OPEN_KEY = 'repairs.descriptionOpen';
const RAIL_MIN = 200;
const RAIL_MAX = 640;
const RAIL_DEFAULT = 320;
const RAIL_KEY_STEP = 24;

function readStored(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeStored(key: string, value: string) {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    // Private window or blocked storage: the setting just isn't remembered.
  }
}

function clampRail(w: number): number {
  return Math.min(RAIL_MAX, Math.max(RAIL_MIN, Math.round(w)));
}

function initialRailWidth(): number {
  const n = Number(readStored(RAIL_WIDTH_KEY));
  return Number.isFinite(n) && n > 0 ? clampRail(n) : RAIL_DEFAULT;
}

// Two lines of the description in the rail; the full text is in the header.
const CLAMP_2 = {
  display: '-webkit-box',
  WebkitLineClamp: 2,
  WebkitBoxOrient: 'vertical',
  overflow: 'hidden',
} as const;

function RailResizer({ width, onResize }: { width: number; onResize: (w: number, persist: boolean) => void }) {
  const start = useRef<{ x: number; w: number } | null>(null);
  return (
    <Box
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize fixer list"
      aria-valuemin={RAIL_MIN}
      aria-valuemax={RAIL_MAX}
      aria-valuenow={width}
      tabIndex={0}
      data-testid="repairs-rail-resizer"
      onPointerDown={(e) => {
        e.preventDefault();
        e.currentTarget.setPointerCapture(e.pointerId);
        start.current = { x: e.clientX, w: width };
      }}
      onPointerMove={(e) => {
        if (!start.current) return;
        onResize(start.current.w + e.clientX - start.current.x, false);
      }}
      onPointerUp={(e) => {
        if (!start.current) return;
        onResize(start.current.w + e.clientX - start.current.x, true);
        start.current = null;
      }}
      onDoubleClick={() => onResize(RAIL_DEFAULT, true)}
      onKeyDown={(e) => {
        if (e.key === 'ArrowLeft') onResize(width - RAIL_KEY_STEP, true);
        else if (e.key === 'ArrowRight') onResize(width + RAIL_KEY_STEP, true);
        else return;
        e.preventDefault();
      }}
      sx={{
        display: { xs: 'none', md: 'block' },
        width: 6,
        flexShrink: 0,
        cursor: 'col-resize',
        touchAction: 'none',
        borderRight: 1,
        borderColor: 'divider',
        '&:hover, &:focus-visible': { bgcolor: 'primary.main', opacity: 0.5, outline: 'none' },
      }}
    />
  );
}

function FixerRail({ repairs, width }: RepairsPanelProps & { width: number }) {
  const { fixers, selectedFixerId, trials } = repairs;
  return (
    <Box
      data-testid="repairs-rail"
      sx={{
        width: { xs: '100%', md: width },
        flexShrink: 0,
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
              <Typography variant="body2" title={f.description} sx={{ color: 'text.secondary', mb: 0.5, ...CLAMP_2 }}>
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
  skipped_fragment: 'Fragment — use the consolidation fixer',
  skipped_possible_fragment: 'Possible fragment (not proven)',
  skipped_needs_manual: 'Needs manual',
  skipped_user_locked: 'User-locked title or author',
  skipped_repair_locked: 'Locked by an earlier repair',
  skipped_provider_title: 'Title from a metadata provider',
  skipped_itunes: 'iTunes library (hands-off)',
  skipped_owner_manual: 'Doctor Who / Big Finish / Torchwood (manual)',
  skipped_guard_unreadable: 'Could not read for the guard',
  skipped_co_owner: 'Another book owns the same file',
  skipped_co_owner_duplicate_copy: 'A second copy of the parent owns the same file (deduplicate first)',
  skipped_existing_book: 'A book of this title exists (durations differ)',
  skipped_retagged_copies: "Re-tagged copies of an existing book's chapters",
  skipped_lone_chapter: 'Fewer than three chapters share its name',
  skipped_scattered_numbered: 'Numbered file left over in a folder of several works',
  skipped_no_chapter_key: 'Chapter title but no chapter number in the file name',
  skipped_unplaced: 'Taken into no chapter group',
  skipped_cleared_by_field_lock: 'Series cleared or overridden (field lock)',
  skipped_cleared_by_manual: 'Series cleared by a user edit',
  skipped_cleared_by_batch: 'Series cleared by a batch edit',
  skipped_cleared_by_undo: 'Series cleared by undo last apply',
  skipped_cleared_by_operation_revert: 'Series cleared by an operation revert',
  skipped_cleared_by_fixer: 'Series cleared by a maintenance fixer',
  skipped_cleared_by_metadata_apply: 'Series cleared by a metadata apply',
  skipped_cleared_by_other: 'Series cleared (other source)',
  skipped_not_stale: 'Series id is set again',
  skipped_gone: 'Book no longer exists',
  skipped_series_mismatch: 'Series evidence disagrees',
  skipped_history_unreadable: 'Could not read series history',
  skipped_no_provider_author: 'No provider author on record',
  skipped_multi_author: 'Several authors — needs a person',
  skipped_implausible_author: 'Provider author is not a plausible name',
  skipped_ambiguous_author: 'Several existing authors match the name',
  skipped_swapped_title_author: 'Title and author swapped — use the swapped fixer',
  skipped_not_swapped: 'No longer swapped',
  skipped_relink_series_first: 'Stale series object — relink the series first',
  skipped_split_refused: 'Splitter will not split the name',
  skipped_implausible_part: 'A part looks like a title or junk',
  skipped_contributor_role: 'Names contributor roles (translator, editor)',
  skipped_doubled_author: 'Same author repeated',
  skipped_anthology: 'Too many names (anthology or cast)',
  skipped_names_a_title: 'The name is a book or series title',
  skipped_not_combined: 'No longer a combined credit',
  skipped_same_audio_other_row: 'Same chapters in another row of the folder',
  skipped_ambiguous: 'Matches more than one parent',
  skipped_copy_unproven: 'Copy not proven (no import path or hash)',
  skipped_moved_unproven: 'Move not proven (name and size only)',
  skipped_duration_gate: 'Files too long to be chapters',
  skipped_duration_unknown: 'Chapter durations unknown',
  skipped_files_missing: 'Files missing on disk',
  skipped_unreadable: 'Could not read a file',
  skipped_track_order: 'Chapter order cannot be told',
  skipped_no_survivor: 'No book can keep the chapters',
  skipped_stranded: 'Emptied by an unfinished repair',
  skipped_numbered_set_unsure: 'Numbered files may be several works',
  skipped_what_if: 'What-if plan (never applied)',
  skipped_interrupted_run: 'An earlier repair of this folder did not finish',
  error: 'Error',
};

/**
 * One chip per skip kind with its count. Each chip pages exactly the rows it
 * counts ("every count opens its books"); "All skipped" goes back. Under a
 * selected row class the counts are that class's (skipped_by_kind_in_class),
 * since a kind chip then pages only the class's rows.
 */
function SkipKindChips({ repairs }: RepairsPanelProps) {
  const counts =
    (repairs.rowClass ? repairs.page?.skipped_by_kind_in_class : repairs.page?.skipped_by_kind) ?? {};
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
      {/* The owner's rows: the whole plan's count, so offered only when no
          class narrows the list (the count must be what the chip opens). */}
      {!repairs.rowClass && (repairs.page?.owner_applicable ?? 0) > 0 && (
        <Chip
          size="small"
          clickable
          label={`Owner apply (${repairs.page?.owner_applicable})`}
          color={repairs.filter === 'owner_applicable' ? 'secondary' : 'default'}
          variant={repairs.filter === 'owner_applicable' ? 'filled' : 'outlined'}
          onClick={() => repairs.setFilter('owner_applicable')}
          data-testid="repairs-skip-kind-owner"
        />
      )}
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

function FixerHeader({ title, description }: { title: string; description: string }) {
  const [open, setOpen] = useState(() => readStored(DESC_OPEN_KEY) === '1');
  const toggle = () => {
    setOpen((o) => {
      writeStored(DESC_OPEN_KEY, o ? '0' : '1');
      return !o;
    });
  };
  return (
    <Box sx={{ px: 2, py: 1, borderBottom: 1, borderColor: 'divider' }} data-testid="repairs-fixer-header">
      <Stack direction="row" sx={{ alignItems: 'baseline', gap: 1 }}>
        <Typography variant="h6" sx={{ flexShrink: 0 }}>
          {title}
        </Typography>
        <Typography
          variant="body2"
          data-testid="repairs-fixer-description"
          sx={{
            color: 'text.secondary',
            flex: 1,
            minWidth: 0,
            ...(open ? {} : { whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }),
          }}
        >
          {description}
        </Typography>
        <Button size="small" onClick={toggle} aria-expanded={open} data-testid="repairs-description-toggle" sx={{ flexShrink: 0 }}>
          {open ? 'Less' : 'More'}
        </Button>
      </Stack>
      {open && (
        <Typography variant="caption" component="div" sx={{ color: 'text.secondary', mt: 0.5 }}>
          A trial writes nothing. Applying writes the chosen rows and records each change in the
          book history. If a library scan is running, it pauses briefly while the rows are written.
        </Typography>
      )}
    </Box>
  );
}

function RowsView({ repairs, viewMode = 'compact' }: RepairsPanelProps) {
  if (viewMode === 'two-column') return <RepairsDetailsView repairs={repairs} />;
  if (viewMode === 'candidates') return <RepairsGroupedView repairs={repairs} />;
  return <RepairsCompactView repairs={repairs} />;
}

function PlanView({ repairs, viewMode }: RepairsPanelProps) {
  const fixer = repairs.selectedFixer;
  const { page, planOpId, trial } = repairs;
  if (!fixer) return null;

  const runTrial = () => repairs.dispatch({ lane: 'repairs', type: 'runTrial', fixerId: fixer.id });
  const running = trial?.phase === 'running';
  // Under a selected class the tabs list only that class's rows, so their
  // labels count only them ("every count opens its books"). The page's own
  // class decides, so a label never counts rows of a class the list is not
  // (yet) showing.
  const inClass = !!page?.class;
  const skippedTotal = sumCounts(
    inClass && page?.skipped_by_kind_in_class ? page.skipped_by_kind_in_class : page?.skipped_by_kind
  );
  const selectedCount = repairs.selectedRowIds.size;
  const applicable = (inClass ? page?.applicable_in_class : undefined) ?? page?.applicable ?? 0;
  const remaining = repairs.remainingApplicable ?? 0;
  const applyDisabled = repairs.applying || running;

  return (
    <Box sx={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
      <FixerHeader title={fixer.title} description={fixer.description} />

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
                    rowClass: repairs.rowClass ?? undefined,
                  })
                }
              >
                {repairs.rowClass
                  ? `Apply all applicable in this class (${remaining})`
                  : `Apply all applicable (${remaining})`}
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
              severity={applySeverity(repairs.applyResult)}
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
              {(repairs.applyResult.retry_later ?? 0) > 0 &&
                '. Rows marked "Retry later" could not be checked right now; apply them again in a few minutes.'}
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
            {page && page.total > 0 && <RowsView repairs={repairs} viewMode={viewMode} />}
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

export function RepairsPanel({ repairs, viewMode = 'compact' }: RepairsPanelProps) {
  const [railWidth, setRailWidth] = useState(initialRailWidth);
  const resizeRail = useCallback((w: number, persist: boolean) => {
    const next = clampRail(w);
    setRailWidth(next);
    if (persist) writeStored(RAIL_WIDTH_KEY, String(next));
  }, []);
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
      <FixerRail repairs={repairs} width={railWidth} />
      <RailResizer width={railWidth} onResize={resizeRail} />
      {repairs.selectedFixer ? (
        <PlanView repairs={repairs} viewMode={viewMode} />
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
