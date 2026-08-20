// file: web/src/components/settings/WriteBackPreviewTable.tsx
// version: 1.0.2
// guid: e8a9b0c1-d2e3-4f5a-6b7c-8d9e0f1a2b3c
// last-edited: 2026-08-19

import {
  Box,
  Chip,
  Paper,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Tooltip,
  Typography,
} from '@mui/material';
import {
  ColumnPicker,
  ResizableHeaderCell,
  useConfigurableTable,
  type ColumnDef,
} from '../common/ConfigurableTable';
import type { ITunesBookMapping } from '../../services/api';

/**
 * Shared preview table for the iTunes write-back dialog. Renders the four
 * path columns (iTunes-current vs AO-going-to-write) plus title/author/
 * status, with click-to-sort and drag-to-resize headers. Column widths
 * persist per-user via localStorage.
 *
 * The four paths exposed to the user:
 *   - iTunes Library Path           — what iTunes currently has (W:/...)
 *   - iTunes Library Translated     — local equivalent of the iTunes path
 *                                     (so the user can stat it / open it)
 *   - Local Path                    — where this app has the file on disk
 *   - Local → iTunes Path           — what this app will write to iTunes
 *
 * Status: "Differs" iff the last column != the first. That's the whole
 * reason the user opened this dialog.
 */
export interface WriteBackPreviewTableProps {
  /** Storage key for column-state persistence (must be unique per usage). */
  storageKey: string;
  /** Rows to render. Empty array renders an empty state. */
  items: ITunesBookMapping[];
  /** Optional max table height for scroll. Default 400. */
  maxHeight?: number | string;
}

const truncatedCell = (value?: string, fallback = '(not in library)') => {
  const display = value || fallback;
  return (
    <Tooltip title={display}>
      <Typography
        variant="body2"
        component="span"
        sx={{
          display: 'block',
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          whiteSpace: 'nowrap',
        }}
      >
        {display}
      </Typography>
    </Tooltip>
  );
};

const COLUMNS: ColumnDef<ITunesBookMapping>[] = [
  {
    key: 'title',
    label: 'Title',
    defaultWidth: 220,
    minWidth: 100,
    sortable: true,
    sortValue: (r) => (r.title || '').toLowerCase(),
    render: (r) => truncatedCell(r.title, '(no title)'),
  },
  {
    key: 'author',
    label: 'Author',
    defaultWidth: 160,
    minWidth: 80,
    sortable: true,
    sortValue: (r) => (r.author || '').toLowerCase(),
    render: (r) => truncatedCell(r.author, '(unknown)'),
  },
  {
    key: 'itunes_path',
    label: 'iTunes Library Path',
    defaultWidth: 240,
    minWidth: 120,
    sortable: true,
    sortValue: (r) => (r.itunes_path || '').toLowerCase(),
    render: (r) => truncatedCell(r.itunes_path),
  },
  {
    key: 'itunes_path_translated',
    label: 'iTunes Library Translated',
    defaultWidth: 240,
    minWidth: 120,
    sortable: true,
    sortValue: (r) => (r.itunes_path_translated || '').toLowerCase(),
    render: (r) => truncatedCell(r.itunes_path_translated),
  },
  {
    key: 'ao_path',
    label: 'Local Path',
    defaultWidth: 240,
    minWidth: 120,
    sortable: true,
    sortValue: (r) => (r.ao_path || r.local_path || '').toLowerCase(),
    render: (r) => truncatedCell(r.ao_path || r.local_path),
  },
  {
    key: 'ao_itunes_translated_path',
    label: 'Local → iTunes Path',
    defaultWidth: 240,
    minWidth: 120,
    sortable: true,
    sortValue: (r) => (r.ao_itunes_translated_path || '').toLowerCase(),
    render: (r) => truncatedCell(r.ao_itunes_translated_path),
  },
  {
    key: 'status',
    label: 'Status',
    defaultWidth: 110,
    minWidth: 80,
    sortable: true,
    sortValue: (r) => (r.path_differs ? 0 : 1),
    render: (r) =>
      r.path_differs ? (
        <Chip label="Differs" color="warning" size="small" />
      ) : (
        <Chip label="Match" color="success" size="small" />
      ),
  },
];

export function WriteBackPreviewTable({
  storageKey,
  items,
  maxHeight = 400,
}: WriteBackPreviewTableProps) {
  const {
    visibleColumns,
    sortField,
    sortDir,
    columnWidths,
    handleSort,
    toggleColumn,
    isColumnVisible,
    startResize,
    sortRows,
    resetColumns,
  } = useConfigurableTable<ITunesBookMapping>({
    storageKey,
    columns: COLUMNS,
    defaultSortField: 'status',
    defaultSortDir: 'asc',
  });

  const sorted = sortRows(items);

  return (
    <Box>
      <Stack
        direction="row"
        spacing={1}
        sx={{
          alignItems: 'center',
          mb: 0.5,
        }}
      >
        <Typography
          variant="caption"
          sx={{
            color: 'text.secondary',
            flex: 1,
          }}
        >
          Drag column edges to resize · Click a header to sort
        </Typography>
        <ColumnPicker
          columns={COLUMNS.map((c) => ({ key: c.key, label: c.label }))}
          isVisible={isColumnVisible}
          onToggle={toggleColumn}
          onReset={resetColumns}
        />
      </Stack>
      <TableContainer component={Paper} variant="outlined" sx={{ maxHeight }}>
        <Table size="small" stickyHeader sx={{ tableLayout: 'fixed' }}>
          <TableHead>
            <TableRow>
              {visibleColumns.map((col) => (
                <ResizableHeaderCell
                  key={col.key}
                  columnKey={col.key}
                  label={col.label}
                  width={columnWidths[col.key] ?? col.defaultWidth ?? 150}
                  align={col.align}
                  sortable={col.sortable !== false}
                  sortActive={sortField === col.key}
                  sortDirection={sortDir}
                  onSort={() => handleSort(col.key)}
                  onStartResize={startResize}
                />
              ))}
            </TableRow>
          </TableHead>
          <TableBody>
            {sorted.map((item) => (
              <TableRow
                key={item.book_id}
                sx={item.path_differs ? { bgcolor: 'warning.50' } : undefined}
              >
                {visibleColumns.map((col) => (
                  <TableCell
                    key={col.key}
                    sx={{
                      width: columnWidths[col.key] ?? col.defaultWidth ?? 150,
                      maxWidth: columnWidths[col.key] ?? col.defaultWidth ?? 150,
                      overflow: 'hidden',
                    }}
                  >
                    {col.render(item)}
                  </TableCell>
                ))}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>
    </Box>
  );
}
