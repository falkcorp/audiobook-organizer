// file: web/src/components/review/repairs/RepairsCompactView.tsx
// version: 1.0.0
// guid: a13c754a-0d7e-470b-8895-93a5b0ad4224
// last-edited: 2026-10-08

/**
 * Compact rows: one line per repairs row (checkbox, title and author, a
 * one-line change summary, the reason headline, risk and result), with a
 * chevron that opens the rest -- full reason, class, members, evidence, and an
 * owner row's own Apply button.
 *
 * The reason is rendered once: as the ellipsized headline while the row is
 * closed, and as the full text in the opened part while it is open.
 */

import type { MouseEvent } from 'react';
import { Box, Chip, IconButton, Stack, Typography } from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import ExpandLessIcon from '@mui/icons-material/ExpandLess';
import type { RepairRow } from '../../../services/api';
import {
  OutcomeChip,
  OwnerApplyCell,
  RiskChip,
  RowCheckbox,
  RowEvidence,
  RowMembers,
  RowTitle,
  type RepairsViewProps,
} from './RowParts';
import { changeSummary, classLabel, isSkippedFilter, rowReason } from './rowHelpers';
import { useExpandedSet } from './useExpandedSet';

const ELLIPSIS = { whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' } as const;

export function CompactRow({
  row,
  repairs,
  expanded,
  onToggle,
}: { row: RepairRow; expanded: boolean; onToggle: (rowId: string) => void } & RepairsViewProps) {
  const skippedTab = isSkippedFilter(repairs.filter);
  const outcome = repairs.rowOutcomes.get(row.row_id);
  const summary = changeSummary(row);
  const reason = rowReason(row, skippedTab);
  const label = row.title || row.row_id;
  // A click anywhere on the line opens the row, except on its own controls.
  const onLineClick = (e: MouseEvent<HTMLElement>) => {
    if ((e.target as HTMLElement).closest('a,button,input,label')) return;
    onToggle(row.row_id);
  };
  return (
    <Box
      data-testid={`repairs-row-${row.row_id}`}
      data-expanded={expanded ? 'true' : 'false'}
      sx={{ borderBottom: 1, borderColor: 'divider' }}
    >
      <Stack
        direction="row"
        spacing={1}
        onClick={onLineClick}
        sx={{ alignItems: 'center', px: 1, minHeight: 40, cursor: 'pointer', '&:hover': { bgcolor: 'action.hover' } }}
      >
        {!skippedTab && (
          <Box sx={{ width: 38, flexShrink: 0 }}>
            <RowCheckbox row={row} repairs={repairs} />
          </Box>
        )}
        <Box sx={{ flex: '2 1 0', minWidth: 0, ...ELLIPSIS }}>
          <RowTitle row={row} />
          {row.author && (
            <Typography component="span" variant="body2" sx={{ color: 'text.secondary', ml: 1 }}>
              {row.author}
            </Typography>
          )}
        </Box>
        <Typography
          variant="body2"
          title={summary}
          data-testid={`repairs-row-summary-${row.row_id}`}
          sx={{ flex: '2 1 0', minWidth: 0, ...ELLIPSIS }}
        >
          {summary}
        </Typography>
        <Typography
          variant="body2"
          title={reason}
          sx={{ flex: '3 1 0', minWidth: 0, color: 'text.secondary', ...ELLIPSIS }}
        >
          {!expanded && reason}
        </Typography>
        <RiskChip risk={row.risk} />
        {row.owner_applicable && row.skipped && (
          <Chip size="small" color="secondary" variant="outlined" label="Owner apply" />
        )}
        {!skippedTab && outcome && <OutcomeChip result={outcome} />}
        <IconButton
          size="small"
          aria-expanded={expanded}
          aria-label={expanded ? `Hide details of ${label}` : `Show details of ${label}`}
          data-testid={`repairs-row-expand-${row.row_id}`}
          onClick={() => onToggle(row.row_id)}
        >
          {expanded ? <ExpandLessIcon fontSize="small" /> : <ExpandMoreIcon fontSize="small" />}
        </IconButton>
      </Stack>
      {expanded && (
        <Box sx={{ pl: skippedTab ? 2 : 6, pr: 2, pb: 1.5 }} data-testid={`repairs-row-detail-${row.row_id}`}>
          <Typography variant="body2" sx={{ wordBreak: 'break-word' }}>
            {reason}
          </Typography>
          {skippedTab && row.skip_reason && row.skipped && (
            <Typography variant="caption" component="div" sx={{ color: 'text.secondary' }}>
              {row.skipped}
            </Typography>
          )}
          {row.class && (
            <Chip size="small" variant="outlined" label={classLabel(row.class)} sx={{ mt: 0.5 }} />
          )}
          <RowMembers row={row} />
          <RowEvidence row={row} />
          {row.owner_applicable && row.skipped && <OwnerApplyCell row={row} repairs={repairs} />}
        </Box>
      )}
    </Box>
  );
}

export function RepairsCompactView({ repairs }: RepairsViewProps) {
  const { expanded, toggle } = useExpandedSet();
  return (
    <Box data-testid="repairs-compact">
      {repairs.rows.map((row) => (
        <CompactRow
          key={row.row_id}
          row={row}
          repairs={repairs}
          expanded={expanded.has(row.row_id)}
          onToggle={toggle}
        />
      ))}
    </Box>
  );
}
