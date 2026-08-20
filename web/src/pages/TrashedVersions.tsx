// file: web/src/pages/TrashedVersions.tsx
// version: 1.1.2
// guid: 6f4a5b3c-7d8e-4a70-b8c5-3d7e0f1b9a99

import { useCallback, useEffect, useState } from 'react';
import {
  Box,
  Button,
  Chip,
  Paper,
  Stack,
  Tab,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Tabs,
  Typography,
} from '@mui/material';
import {
  type BookVersion,
  restoreVersion,
  purgeVersion,
  hardDeleteVersion,
} from '../services/versionApi';
import {
  useConfigurableTable,
  ResizableHeaderCell,
  ColumnPicker,
  type ColumnDef,
} from '../components/common/ConfigurableTable';

const API_BASE = '/api/v1';

const COLUMNS: ColumnDef<BookVersion>[] = [
  {
    key: 'book_id',
    label: 'Book ID',
    defaultWidth: 140,
    sortable: true,
    render: (v) => (
      <Typography variant="caption" fontFamily="monospace">
        {v.book_id.slice(0, 12)}…
      </Typography>
    ),
    sortValue: (v) => v.book_id,
  },
  {
    key: 'format',
    label: 'Format',
    defaultWidth: 90,
    sortable: true,
    render: (v) => v.format?.toUpperCase() ?? '—',
    sortValue: (v) => v.format ?? '',
  },
  {
    key: 'source',
    label: 'Source',
    defaultWidth: 120,
    sortable: true,
    render: (v) => v.source,
    sortValue: (v) => v.source,
  },
  {
    key: 'status',
    label: 'Status',
    defaultWidth: 120,
    sortable: true,
    render: (v) => (
      <Chip label={v.status} size="small" color={v.status === 'trash' ? 'warning' : 'error'} />
    ),
    sortValue: (v) => v.status,
  },
  {
    key: 'date',
    label: 'Date',
    defaultWidth: 110,
    sortable: true,
    render: (v) => new Date(v.purged_date || v.created_at).toLocaleDateString(),
    sortValue: (v) => new Date(v.purged_date || v.created_at).getTime(),
  },
];

export default function TrashedVersions() {
  const [tab, setTab] = useState<'trash' | 'purged'>('trash');
  const [versions, setVersions] = useState<BookVersion[]>([]);
  const [loading, setLoading] = useState(true);

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
  } = useConfigurableTable<BookVersion>({
    storageKey: 'trashed-versions',
    columns: COLUMNS,
    defaultSortField: 'date',
    defaultSortDir: 'desc',
  });

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const endpoint =
        tab === 'trash' ? '/audiobooks/trashed-versions' : '/audiobooks/purged-versions';
      const resp = await fetch(`${API_BASE}${endpoint}`);
      if (resp.ok) {
        const data = await resp.json();
        setVersions(data.versions || []);
      }
    } catch {
      setVersions([]);
    } finally {
      setLoading(false);
    }
  }, [tab]);

  useEffect(() => {
    load();
  }, [load]);

  const handleRestore = useCallback(
    async (v: BookVersion) => {
      await restoreVersion(v.book_id, v.id);
      load();
    },
    [load]
  );

  const handlePurge = useCallback(
    async (v: BookVersion) => {
      if (!confirm('Permanently delete files for this version? This cannot be undone.')) return;
      await purgeVersion(v.book_id, v.id);
      load();
    },
    [load]
  );

  const handleHardDelete = useCallback(
    async (v: BookVersion) => {
      if (!confirm('Remove all traces of this version? Fingerprint data will be lost.')) return;
      await hardDeleteVersion(v.id);
      load();
    },
    [load]
  );

  const sorted = sortRows(versions);

  return (
    <Box sx={{ p: 3 }}>
      <Stack direction="row" alignItems="center" justifyContent="space-between" sx={{ mb: 2 }}>
        <Typography variant="h4">Version Management</Typography>
        <ColumnPicker
          columns={allColumns}
          isVisible={isColumnVisible}
          onToggle={toggleColumn}
          onReset={resetColumns}
        />
      </Stack>

      <Tabs
        value={tab}
        onChange={(_, v) => setTab(v)}
        variant="scrollable"
        scrollButtons="auto"
        allowScrollButtonsMobile
        sx={{ mb: 2 }}
      >
        <Tab label="Trash" value="trash" />
        <Tab label="Purged" value="purged" />
      </Tabs>

      {loading ? (
        <Typography color="text.secondary">Loading...</Typography>
      ) : versions.length === 0 ? (
        <Typography color="text.secondary">
          {tab === 'trash' ? 'No trashed versions.' : 'No purged versions.'}
        </Typography>
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
                <TableCell align="right" sx={{ width: 160 }}>
                  Actions
                </TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {sorted.map((v) => (
                <TableRow key={v.id}>
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
                      {col.render(v)}
                    </TableCell>
                  ))}
                  <TableCell align="right">
                    {v.status === 'trash' && (
                      <>
                        <Button size="small" onClick={() => handleRestore(v)}>
                          Restore
                        </Button>
                        <Button size="small" color="error" onClick={() => handlePurge(v)}>
                          Purge Now
                        </Button>
                      </>
                    )}
                    {(v.status === 'inactive_purged' || v.status === 'blocked_for_redownload') && (
                      <Button size="small" color="error" onClick={() => handleHardDelete(v)}>
                        Hard Delete
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Box>
  );
}
