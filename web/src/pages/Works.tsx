// file: web/src/pages/Works.tsx
// version: 1.2.2
// guid: 4b5c6d7e-8f9a-0b1c-2d3e-4f5a6b7c8d9e

import { useCallback, useEffect, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Paper,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from '@mui/material';
import * as api from '../services/api';
import {
  useConfigurableTable,
  ResizableHeaderCell,
  ColumnPicker,
  type ColumnDef,
} from '../components/common/ConfigurableTable';

const COLUMNS: ColumnDef<api.Work>[] = [
  {
    key: 'title',
    label: 'Title',
    defaultWidth: 300,
    sortable: true,
    render: (w) => w.title || 'Untitled',
    sortValue: (w) => w.title ?? '',
  },
  {
    key: 'id',
    label: 'Work ID',
    defaultWidth: 230,
    sortable: true,
    render: (w) => w.id,
    sortValue: (w) => w.id,
  },
  {
    key: 'author_id',
    label: 'Author ID',
    defaultWidth: 100,
    sortable: true,
    render: (w) => w.author_id ?? '—',
    sortValue: (w) => w.author_id ?? 0,
  },
  {
    key: 'series_id',
    label: 'Series ID',
    defaultWidth: 100,
    sortable: true,
    render: (w) => w.series_id ?? '—',
    sortValue: (w) => w.series_id ?? 0,
  },
  {
    key: 'alt_titles',
    label: 'Alternate Titles',
    defaultWidth: 120,
    sortable: true,
    render: (w) => w.alt_titles?.length ?? 0,
    sortValue: (w) => w.alt_titles?.length ?? 0,
  },
];

export function Works() {
  const [works, setWorks] = useState<api.Work[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

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
  } = useConfigurableTable<api.Work>({
    storageKey: 'works',
    columns: COLUMNS,
    defaultSortField: 'title',
  });

  const loadWorks = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await api.getWorks();
      setWorks(data);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load works');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadWorks();
  }, [loadWorks]);

  if (loading) {
    return (
      <Box sx={{ py: 6, display: 'flex', justifyContent: 'center' }}>
        <CircularProgress />
      </Box>
    );
  }

  if (error) {
    return (
      <Stack spacing={2}>
        <Typography variant="h4">Works</Typography>
        <Alert
          severity="error"
          action={
            <Button color="inherit" size="small" onClick={() => void loadWorks()}>
              Retry
            </Button>
          }
        >
          {error}
        </Alert>
      </Stack>
    );
  }

  const sorted = sortRows(works);

  return (
    <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      <Stack
        direction="row"
        sx={{
          alignItems: 'center',
          justifyContent: 'space-between',
        }}
      >
        <Box>
          <Typography variant="h4" gutterBottom>
            Works
          </Typography>
          <Typography
            variant="body2"
            sx={{
              color: 'text.secondary',
            }}
          >
            Logical title-level groupings across editions and narrations.
          </Typography>
        </Box>
        <ColumnPicker
          columns={allColumns}
          isVisible={isColumnVisible}
          onToggle={toggleColumn}
          onReset={resetColumns}
        />
      </Stack>

      {works.length === 0 ? (
        <Alert severity="info">
          No works found yet. Works are created during scans and metadata imports.
        </Alert>
      ) : (
        <TableContainer component={Paper}>
          <Table size="small" sx={{ tableLayout: 'fixed' }}>
            <TableHead>
              <TableRow>
                {visibleColumns.map((col) => (
                  <ResizableHeaderCell
                    key={col.key}
                    columnKey={col.key}
                    label={col.label}
                    width={columnWidths[col.key] ?? col.defaultWidth ?? 150}
                    sortable={col.sortable}
                    sortActive={sortField === col.key}
                    sortDirection={sortDir}
                    onSort={() => handleSort(col.key)}
                    onStartResize={startResize}
                  />
                ))}
              </TableRow>
            </TableHead>
            <TableBody>
              {sorted.map((work) => (
                <TableRow key={work.id} hover>
                  {visibleColumns.map((col) => (
                    <TableCell
                      key={col.key}
                      align={col.align}
                      sx={{
                        width: columnWidths[col.key],
                        maxWidth: columnWidths[col.key],
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        whiteSpace: 'nowrap',
                      }}
                    >
                      {col.render(work)}
                    </TableCell>
                  ))}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Box>
  );
}
