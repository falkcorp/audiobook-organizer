// file: web/src/components/review/repairs/RowParts.tsx
// version: 1.0.0
// guid: 09def8d7-b23c-49be-80d7-cfdb4a959792
// last-edited: 2026-10-08

/**
 * Pieces of a repairs row that every view renders the same way: its title
 * link, checkbox, risk and outcome chips, members, evidence, and an owner
 * row's own Apply button. Moved here from RepairsPanel.tsx when the rows grew
 * three views (Compact, Details, Grouped).
 */

import { useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';
import { Box, Button, Checkbox, Chip, Link, Stack, Tooltip, Typography } from '@mui/material';
import type { RepairOutcome, RepairRow, RepairRowResult } from '../../../services/api';
import type { RepairsLane } from '../lanes/useRepairsLane';
import { isSelectableRow, memberCounts, rowMembers } from './rowHelpers';

export interface RepairsViewProps {
  repairs: RepairsLane;
}

const OUTCOME_LABEL: Record<RepairOutcome, string> = {
  applied: 'Applied',
  would_apply: 'Would apply (preview)',
  changed_since_plan: 'Changed since trial',
  partially_applied: 'Partly applied',
  skipped_guard: 'Skipped by guard',
  not_applicable: 'Not applicable',
  failed: 'Failed',
  aborted_standdown_lost: 'Stopped: scan resumed',
  retry_later: 'Retry later (nothing written)',
  owner_apply_refused: 'Owner apply refused',
};

const OUTCOME_COLOR: Record<RepairOutcome, 'success' | 'info' | 'warning' | 'error' | 'default'> = {
  applied: 'success',
  would_apply: 'info',
  changed_since_plan: 'warning',
  partially_applied: 'warning',
  skipped_guard: 'default',
  not_applicable: 'default',
  failed: 'error',
  aborted_standdown_lost: 'error',
  retry_later: 'warning',
  owner_apply_refused: 'error',
};

export function OutcomeChip({ result }: { result: RepairRowResult }) {
  // The server may send an outcome newer than this build knows: the lookups
  // answer undefined for it and the fallbacks below show it raw.
  const outcome = result.outcome as RepairOutcome;
  const chip = (
    <Chip
      size="small"
      variant="outlined"
      color={OUTCOME_COLOR[outcome] ?? 'default'}
      label={OUTCOME_LABEL[outcome] ?? result.outcome}
      data-testid={`repairs-outcome-${result.row_id}`}
    />
  );
  const detail = result.error || result.skipped;
  return detail ? <Tooltip title={detail}>{chip}</Tooltip> : chip;
}

export function RiskChip({ risk }: { risk: string }) {
  return (
    <Chip
      size="small"
      label={risk === 'review' ? 'Review' : risk === 'low' ? 'Low' : risk}
      color={risk === 'review' ? 'warning' : 'default'}
      variant={risk === 'low' ? 'outlined' : 'filled'}
    />
  );
}

/** The row's title, linked to its first book when it has one. */
export function RowTitle({ row }: { row: RepairRow }) {
  const bookId = row.book_ids[0];
  if (!bookId) return <>{row.title || row.row_id}</>;
  return (
    <Link component={RouterLink} to={`/library/${encodeURIComponent(bookId)}`}>
      {row.title || bookId}
    </Link>
  );
}

/**
 * The row's checkbox, or nothing when the row may not be ticked. Callers
 * render it only off the skipped tab; the selectable guard is here too so a
 * mixed page cannot offer one.
 */
export function RowCheckbox({ row, repairs }: { row: RepairRow } & RepairsViewProps) {
  if (!isSelectableRow(row, repairs.settledRowIds)) return null;
  return (
    <Checkbox
      size="small"
      checked={repairs.selectedRowIds.has(row.row_id)}
      onChange={() => repairs.toggleRow(row.row_id)}
      slotProps={{ input: { 'aria-label': `Select ${row.title || row.row_id}` } }}
    />
  );
}

/**
 * Every book of a row, each a link to the book, with its role and file
 * counts. Rows with one book show nothing extra (the title links it); rows
 * with more show a "N books" toggle that lists them all.
 */
export function RowMembers({ row }: { row: RepairRow }) {
  const [open, setOpen] = useState(false);
  const members = rowMembers(row);
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
                {memberCounts(m)}
              </Typography>
            </li>
          ))}
        </Box>
      )}
    </Box>
  );
}

const EVIDENCE_SHOWN = 3;

/**
 * What the row's decision was made from. Long lists fold behind a toggle
 * unless `fold` is false (the Details view shows every line).
 */
export function RowEvidence({ row, fold = true }: { row: RepairRow; fold?: boolean }) {
  const [all, setAll] = useState(false);
  const ev = row.evidence ?? [];
  if (ev.length === 0) return null;
  const shownEv = !fold || all ? ev : ev.slice(0, EVIDENCE_SHOWN);
  return (
    <Box data-testid={`repairs-row-evidence-${row.row_id}`}>
      {shownEv.map((e, i) => (
        <Typography key={i} variant="caption" component="div" sx={{ color: 'text.secondary', wordBreak: 'break-all' }}>
          {e}
        </Typography>
      ))}
      {fold && ev.length > EVIDENCE_SHOWN && (
        <Button size="small" onClick={() => setAll((v) => !v)} sx={{ p: 0, minWidth: 0, textTransform: 'none' }}>
          {all ? 'Show less' : `Show all ${ev.length}`}
        </Button>
      )}
    </Box>
  );
}

/**
 * An owner row's proof and its own Apply button. One row per click, with a
 * confirm naming the book (the lane's dispatch asks); no checkbox, so no
 * selection or "apply all" ever includes it.
 */
export function OwnerApplyCell({ row, repairs }: { row: RepairRow } & RepairsViewProps) {
  const fixer = repairs.selectedFixer;
  const planOpId = repairs.planOpId;
  const outcome = repairs.rowOutcomes.get(row.row_id);
  const settled = repairs.settledRowIds.has(row.row_id);
  return (
    <Box
      sx={{ mt: 1, p: 1, border: 1, borderColor: 'secondary.main', borderRadius: 1 }}
      data-testid={`repairs-owner-${row.row_id}`}
    >
      <Typography variant="caption" component="div" sx={{ fontWeight: 600 }}>
        Owner apply: iTunes tracks these files; only database rows change
      </Typography>
      {row.owner_apply_reason && (
        <Typography variant="caption" component="div" sx={{ color: 'text.secondary' }}>
          {row.owner_apply_reason}
        </Typography>
      )}
      <Stack direction="row" spacing={1} sx={{ mt: 0.5, alignItems: 'center' }}>
        {!settled && fixer && planOpId && (
          <Button
            size="small"
            variant="outlined"
            color="secondary"
            disabled={
              repairs.applying ||
              repairs.trial?.phase === 'running' ||
              repairs.ownerStatus?.allowed === false
            }
            data-testid={`repairs-owner-apply-${row.row_id}`}
            onClick={() =>
              repairs.dispatch({
                lane: 'repairs',
                type: 'ownerApplyRow',
                fixerId: fixer.id,
                planOpId,
                rowId: row.row_id,
                title: row.title || row.book_ids[0] || row.row_id,
                fragments: row.owner_writes?.length ?? 1,
              })
            }
          >
            Apply (owner)
          </Button>
        )}
        {outcome && <OutcomeChip result={outcome} />}
      </Stack>
      {!settled && repairs.ownerStatus?.allowed === false && (
        <Typography
          variant="caption"
          component="div"
          sx={{ mt: 0.5, color: 'warning.main' }}
          data-testid={`repairs-owner-why-not-${row.row_id}`}
        >
          {repairs.ownerStatus.reason ||
            `Owner actions need you to sign in through Cloudflare Access (${window.location.host}).`}
        </Typography>
      )}
    </Box>
  );
}
