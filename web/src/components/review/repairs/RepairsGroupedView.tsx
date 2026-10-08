// file: web/src/components/review/repairs/RepairsGroupedView.tsx
// version: 1.0.0
// guid: 1b25c3ed-d0f6-4bba-b8d6-48e102e77e18
// last-edited: 2026-10-08

/**
 * Grouped by fix: the page's rows grouped by class and reason (the same
 * reason is the same fix), biggest group first. Each group can be ticked as a
 * whole and applied with "Apply group", which goes through the same
 * apply-selected dispatch (and the same confirm) as "Apply selected".
 *
 * Groups cover only the loaded page; the caption says so.
 */

import { useMemo } from 'react';
import { Box, Button, Checkbox, Chip, IconButton, Stack, Typography } from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import ExpandLessIcon from '@mui/icons-material/ExpandLess';
import { CompactRow } from './RepairsCompactView';
import type { RepairsViewProps } from './RowParts';
import {
  changeSummary,
  classLabel,
  groupRows,
  isSelectableRow,
  isSkippedFilter,
  titlesPreview,
  type RepairRowGroup,
} from './rowHelpers';
import { useExpandedSet } from './useExpandedSet';

function GroupSection({
  group,
  index,
  repairs,
  open,
  onToggleOpen,
  expandedRows,
  onToggleRow,
}: {
  group: RepairRowGroup;
  index: number;
  open: boolean;
  onToggleOpen: (key: string) => void;
  expandedRows: ReadonlySet<string>;
  onToggleRow: (rowId: string) => void;
} & RepairsViewProps) {
  const skippedTab = isSkippedFilter(repairs.filter);
  const selectable = group.rows
    .filter((r) => isSelectableRow(r, repairs.settledRowIds))
    .map((r) => r.row_id);
  const nSelected = selectable.filter((id) => repairs.selectedRowIds.has(id)).length;
  const all = selectable.length > 0 && nSelected === selectable.length;
  const some = nSelected > 0 && !all;

  const summaries = [...new Set(group.rows.map(changeSummary))];
  const summary = summaries.length === 1 ? summaries[0] : `${summaries[0]} (varies by row)`;

  const fixer = repairs.selectedFixer;
  const planOpId = repairs.planOpId;
  const applyDisabled =
    repairs.applying || repairs.trial?.phase === 'running' || selectable.length === 0 || !fixer || !planOpId;

  const applyGroup = () => {
    if (!fixer || !planOpId || selectable.length === 0) return;
    // The selection shows exactly the group; the dispatch is sent the same
    // ids directly (state set here is not readable until the next render).
    repairs.clearSelection();
    repairs.selectRows(selectable, true);
    repairs.dispatch({ lane: 'repairs', type: 'applyRows', fixerId: fixer.id, planOpId, rowIds: selectable });
  };

  const tid = `repairs-group-${index}`;
  return (
    <Box data-testid={tid} sx={{ borderBottom: 1, borderColor: 'divider' }}>
      <Stack direction="row" spacing={1} useFlexGap sx={{ alignItems: 'center', px: 1, py: 0.5, flexWrap: 'wrap' }}>
        {!skippedTab && (
          <Checkbox
            size="small"
            checked={all}
            indeterminate={some}
            disabled={selectable.length === 0}
            onChange={() => repairs.selectRows(selectable, !all)}
            slotProps={{ input: { 'aria-label': `Select group: ${group.reason}` } }}
            data-testid={`${tid}-select`}
          />
        )}
        <Box sx={{ flex: 1, minWidth: 200 }}>
          <Typography variant="subtitle2" sx={{ wordBreak: 'break-word' }}>
            {group.reason}
          </Typography>
          <Typography
            variant="body2"
            title={summary}
            sx={{ color: 'text.secondary', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}
          >
            {summary}
          </Typography>
        </Box>
        {group.rowClass && <Chip size="small" variant="outlined" label={classLabel(group.rowClass)} />}
        <Chip size="small" label={`${group.rows.length} row${group.rows.length === 1 ? '' : 's'}`} data-testid={`${tid}-count`} />
        {!skippedTab && (
          <Button
            size="small"
            variant="outlined"
            disabled={applyDisabled}
            onClick={applyGroup}
            data-testid={`${tid}-apply`}
          >
            Apply group ({selectable.length})
          </Button>
        )}
        <IconButton
          size="small"
          aria-expanded={open}
          aria-label={open ? `Hide rows of group: ${group.reason}` : `Show rows of group: ${group.reason}`}
          onClick={() => onToggleOpen(group.key)}
          data-testid={`${tid}-expand`}
        >
          {open ? <ExpandLessIcon fontSize="small" /> : <ExpandMoreIcon fontSize="small" />}
        </IconButton>
      </Stack>
      {open ? (
        <Box sx={{ pl: 2 }}>
          {group.rows.map((row) => (
            <CompactRow
              key={row.row_id}
              row={row}
              repairs={repairs}
              expanded={expandedRows.has(row.row_id)}
              onToggle={onToggleRow}
            />
          ))}
        </Box>
      ) : (
        <Typography
          variant="body2"
          sx={{ px: 2, pb: 1, color: 'text.secondary', wordBreak: 'break-word' }}
          data-testid={`${tid}-titles`}
        >
          {titlesPreview(group.rows)}
        </Typography>
      )}
    </Box>
  );
}

export function RepairsGroupedView({ repairs }: RepairsViewProps) {
  const bySkipReason = isSkippedFilter(repairs.filter);
  const groups = useMemo(() => groupRows(repairs.rows, bySkipReason), [repairs.rows, bySkipReason]);
  const openGroups = useExpandedSet();
  const openRows = useExpandedSet();
  return (
    <Box data-testid="repairs-grouped">
      <Typography variant="caption" component="div" sx={{ px: 2, py: 1, color: 'text.secondary' }}>
        Groups cover the rows on this page. Set Rows per page to 500 to group more.
      </Typography>
      {groups.map((g, i) => (
        <GroupSection
          key={g.key}
          group={g}
          index={i}
          repairs={repairs}
          open={openGroups.expanded.has(g.key)}
          onToggleOpen={openGroups.toggle}
          expandedRows={openRows.expanded}
          onToggleRow={openRows.toggle}
        />
      ))}
    </Box>
  );
}
