// file: web/src/pages/Series.tsx
// version: 1.4.1
// guid: 7d8e9f0a-1b2c-3d4e-5f6a-7b8c9d0e1f2a

import { useState, useEffect, useMemo, useCallback } from 'react';
import {
  Box,
  Typography,
  TextField,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TablePagination,
  Paper,
  IconButton,
  Collapse,
  Checkbox,
  Button,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  FormControl,
  FormControlLabel,
  InputLabel,
  Select,
  MenuItem,
  Alert,
  Snackbar,
  Chip,
  CircularProgress,
  Tooltip,
  List,
  ListItem,
  ListItemText,
  ListItemIcon,
  Radio,
  RadioGroup,
  Drawer,
  Divider,
  Badge,
} from '@mui/material';
import KeyboardArrowDownIcon from '@mui/icons-material/KeyboardArrowDown';
import KeyboardArrowUpIcon from '@mui/icons-material/KeyboardArrowUp';
import EditIcon from '@mui/icons-material/Edit';
import DeleteIcon from '@mui/icons-material/Delete';
import CallSplitIcon from '@mui/icons-material/CallSplit';
import MergeTypeIcon from '@mui/icons-material/MergeType';
import RefreshIcon from '@mui/icons-material/Refresh';
import HistoryIcon from '@mui/icons-material/History';
import UndoIcon from '@mui/icons-material/Undo';
import { useNavigate } from 'react-router-dom';
import {
  useConfigurableTable,
  ResizableHeaderCell,
  ColumnPicker,
  type ColumnDef,
} from '../components/common/ConfigurableTable';
import * as api from '../services/api';
import type { SeriesWithCount as SeriesType, Book } from '../services/api';

type SeriesFilter = 'with_books' | 'empty' | 'all';

interface ExpandedRow {
  books: Book[];
  loading: boolean;
}

interface ActionHistoryEntry {
  id: string;
  action: 'rename' | 'merge' | 'split' | 'delete';
  description: string;
  timestamp: Date;
  undoable: boolean;
  undoData?: { seriesId: number; oldName: string };
}

// --- Column definitions ---

const seriesColumns: ColumnDef<SeriesType>[] = [
  {
    key: 'name',
    label: 'Name',
    defaultVisible: true,
    defaultWidth: 300,
    minWidth: 120,
    sortable: true,
    sortValue: (s) => s.name.toLowerCase(),
    render: (s) => (
      <Typography variant="body2" fontWeight={500}>{s.name}</Typography>
    ),
  },
  {
    key: 'author_name',
    label: 'Author',
    defaultVisible: true,
    defaultWidth: 200,
    minWidth: 100,
    sortable: true,
    sortValue: (s) => (s.author_name ?? '').toLowerCase(),
    render: (s) => (
      <Typography variant="body2" color="text.secondary">{s.author_name ?? '-'}</Typography>
    ),
  },
  {
    key: 'book_count',
    label: 'Books',
    defaultVisible: true,
    defaultWidth: 120,
    minWidth: 80,
    align: 'right',
    sortable: true,
    sortValue: (s) => s.book_count ?? 0,
    render: (s) => {
      const count = s.book_count ?? 0;
      const fileCount = s.file_count ?? 0;
      const label = fileCount > count ? `${count} (${fileCount} files)` : `${count}`;
      return <Chip label={label} size="small" variant={count === 0 ? 'outlined' : 'filled'} />;
    },
  },
  {
    key: 'id',
    label: 'ID',
    defaultVisible: false,
    defaultWidth: 70,
    minWidth: 50,
    align: 'right',
    sortable: true,
    sortValue: (s) => s.id,
    render: (s) => s.id,
  },
  {
    key: 'created_at',
    label: 'Created',
    defaultVisible: false,
    defaultWidth: 140,
    minWidth: 100,
    sortable: true,
    sortValue: (s) => s.created_at ?? '',
    render: (s) => s.created_at ? new Date(s.created_at).toLocaleDateString() : '-',
  },
];

// --- SeriesRow ---

interface SeriesRowProps {
  series: SeriesType;
  selected: boolean;
  expanded?: ExpandedRow;
  visibleColumns: ColumnDef<SeriesType>[];
  columnWidths: Record<string, number>;
  onToggleSelect: () => void;
  onToggleExpand: () => void;
  onRename: () => void;
  onSplit: () => void;
  onDelete: () => void;
}

function SeriesRow({ series, selected, expanded, visibleColumns, columnWidths, onToggleSelect, onToggleExpand, onRename, onSplit, onDelete }: SeriesRowProps) {
  const navigate = useNavigate();
  const isExpanded = !!expanded;
  const bookCount = series.book_count ?? 0;
  const totalCols = visibleColumns.length + 3; // checkbox + expand + actions

  return (
    <>
      <TableRow hover sx={{ cursor: bookCount > 0 ? 'pointer' : 'default', '& > *': { borderBottom: isExpanded ? 'unset' : undefined } }} onClick={(e) => { if (bookCount > 0 && !(e.target as HTMLElement).closest('button, input, .MuiCheckbox-root')) onToggleExpand(); }}>
        <TableCell padding="checkbox" sx={{ width: 42 }}>
          <Checkbox checked={selected} onChange={onToggleSelect} />
        </TableCell>
        <TableCell sx={{ width: 42 }}>
          <IconButton size="small" onClick={(e) => { e.stopPropagation(); onToggleExpand(); }} disabled={bookCount === 0}>
            {isExpanded ? <KeyboardArrowUpIcon /> : <KeyboardArrowDownIcon />}
          </IconButton>
        </TableCell>
        {visibleColumns.map((col) => (
          <TableCell
            key={col.key}
            align={col.align}
            sx={{ width: columnWidths[col.key], maxWidth: columnWidths[col.key], overflow: 'hidden', textOverflow: 'ellipsis' }}
          >
            {col.render(series)}
          </TableCell>
        ))}
        <TableCell align="right" sx={{ width: 120, whiteSpace: 'nowrap' }} onClick={(e) => e.stopPropagation()}>
          <Tooltip title="Rename">
            <IconButton size="small" onClick={onRename}><EditIcon fontSize="small" /></IconButton>
          </Tooltip>
          {bookCount > 1 && (
            <Tooltip title="Split">
              <IconButton size="small" onClick={onSplit}><CallSplitIcon fontSize="small" /></IconButton>
            </Tooltip>
          )}
          {bookCount === 0 && (
            <Tooltip title="Delete">
              <IconButton size="small" onClick={onDelete} color="error"><DeleteIcon fontSize="small" /></IconButton>
            </Tooltip>
          )}
        </TableCell>
      </TableRow>
      {isExpanded && (
        <TableRow>
          <TableCell colSpan={totalCols} sx={{ py: 0 }}>
            <Collapse in={isExpanded} timeout="auto" unmountOnExit>
              <Box sx={{ py: 1, px: 3 }}>
                {expanded?.loading ? (
                  <CircularProgress size={20} />
                ) : (
                  <Table size="small">
                    <TableHead>
                      <TableRow>
                        <TableCell>#</TableCell>
                        <TableCell>Title</TableCell>
                        <TableCell>Author</TableCell>
                        <TableCell>Format</TableCell>
                        <TableCell>Duration</TableCell>
                        <TableCell>File Path</TableCell>
                      </TableRow>
                    </TableHead>
                    <TableBody>
                      {expanded?.books.map((book) => (
                        <TableRow key={book.id} hover sx={{ cursor: 'pointer' }} onClick={() => navigate(`/library/${book.id}`)}>
                          <TableCell>{book.series_position ?? '-'}</TableCell>
                          <TableCell>
                            <Typography variant="body2" color="primary" sx={{ '&:hover': { textDecoration: 'underline' } }}>
                              {book.title}
                            </Typography>
                          </TableCell>
                          <TableCell>
                            <Typography variant="body2" color="text.secondary" noWrap>{book.author_name ?? '-'}</Typography>
                          </TableCell>
                          <TableCell>{book.format ?? '-'}</TableCell>
                          <TableCell>
                            {book.duration ? `${Math.floor(book.duration / 3600)}h ${Math.floor((book.duration % 3600) / 60)}m` : '-'}
                          </TableCell>
                          <TableCell>
                            <Tooltip title={book.file_path || ''}>
                              <Typography variant="body2" color="text.secondary" noWrap sx={{ maxWidth: 300 }}>
                                {book.file_path ? book.file_path.split('/').slice(-2).join('/') : '-'}
                              </Typography>
                            </Tooltip>
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                )}
              </Box>
            </Collapse>
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

// --- Main Component ---

export function Series() {
  const [seriesList, setSeriesList] = useState<SeriesType[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [filter, setFilter] = useState<SeriesFilter>('with_books');
  const [page, setPage] = useState(0);
  const [rowsPerPage, setRowsPerPage] = useState(25);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [expanded, setExpanded] = useState<Record<number, ExpandedRow>>({});

  // Dialog states
  const [renameDialog, setRenameDialog] = useState<{ open: boolean; series: SeriesType | null }>({ open: false, series: null });
  const [renameValue, setRenameValue] = useState('');
  const [mergeDialog, setMergeDialog] = useState(false);
  const [mergeKeepId, setMergeKeepId] = useState<number | null>(null);
  const [mergeCustomName, setMergeCustomName] = useState('');
  const [mergeUseCustomName, setMergeUseCustomName] = useState(false);
  const [splitDialog, setSplitDialog] = useState<{ open: boolean; series: SeriesType | null }>({ open: false, series: null });
  const [splitBooks, setSplitBooks] = useState<Book[]>([]);
  const [splitSelected, setSplitSelected] = useState<Set<string>>(new Set());
  const [splitLoading, setSplitLoading] = useState(false);
  const [deleteDialog, setDeleteDialog] = useState<{ open: boolean; series: SeriesType | null }>({ open: false, series: null });
  const [bulkDeleteDialog, setBulkDeleteDialog] = useState(false);
  const [snackbar, setSnackbar] = useState<{ open: boolean; message: string; severity: 'success' | 'error'; undoAction?: () => void }>({ open: false, message: '', severity: 'success' });
  const [history, setHistory] = useState<ActionHistoryEntry[]>([]);
  const [historyOpen, setHistoryOpen] = useState(false);

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
  } = useConfigurableTable<SeriesType>({
    storageKey: 'series',
    columns: seriesColumns,
    defaultSortField: 'name',
    defaultSortDir: 'asc',
  });

  const addHistory = (entry: Omit<ActionHistoryEntry, 'id' | 'timestamp'>) => {
    setHistory((prev) => [{ ...entry, id: crypto.randomUUID(), timestamp: new Date() }, ...prev].slice(0, 50));
  };

  const fetchSeries = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await api.getSeries();
      setSeriesList(data);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load series');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchSeries();
  }, [fetchSeries]);

  const filtered = useMemo(() => {
    let result = seriesList;
    if (filter === 'with_books') {
      result = result.filter((s) => (s.book_count ?? 0) > 0);
    } else if (filter === 'empty') {
      result = result.filter((s) => (s.book_count ?? 0) === 0);
    }
    if (search.trim()) {
      const q = search.toLowerCase();
      result = result.filter(
        (s) =>
          s.name.toLowerCase().includes(q) ||
          (s.author_name && s.author_name.toLowerCase().includes(q))
      );
    }
    return sortRows(result);
  }, [seriesList, search, filter, sortRows]);

  const paged = useMemo(() => {
    const start = page * rowsPerPage;
    return filtered.slice(start, start + rowsPerPage);
  }, [filtered, page, rowsPerPage]);

  const toggleExpand = async (seriesId: number) => {
    if (expanded[seriesId]) {
      setExpanded((prev) => {
        const next = { ...prev };
        delete next[seriesId];
        return next;
      });
      return;
    }
    setExpanded((prev) => ({ ...prev, [seriesId]: { books: [], loading: true } }));
    try {
      const books = await api.getSeriesBooks(seriesId);
      setExpanded((prev) => ({ ...prev, [seriesId]: { books, loading: false } }));
    } catch {
      setExpanded((prev) => ({ ...prev, [seriesId]: { books: [], loading: false } }));
    }
  };

  const toggleSelect = (id: number) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const toggleSelectAll = () => {
    if (selected.size === paged.length) {
      setSelected(new Set());
    } else {
      setSelected(new Set(paged.map((s) => s.id)));
    }
  };

  // Rename
  const openRename = (s: SeriesType) => {
    setRenameDialog({ open: true, series: s });
    setRenameValue(s.name);
  };

  const doRename = async () => {
    if (!renameDialog.series || !renameValue.trim()) return;
    const oldName = renameDialog.series.name;
    const seriesId = renameDialog.series.id;
    const newName = renameValue.trim();
    try {
      await api.renameSeries(seriesId, newName);
      addHistory({ action: 'rename', description: `Renamed "${oldName}" → "${newName}"`, undoable: true, undoData: { seriesId, oldName } });
      const undoFn = async () => {
        try {
          await api.renameSeries(seriesId, oldName);
          addHistory({ action: 'rename', description: `Undo: renamed "${newName}" back to "${oldName}"`, undoable: false });
          setSnackbar({ open: true, message: 'Rename undone', severity: 'success' });
          fetchSeries();
        } catch { setSnackbar({ open: true, message: 'Undo failed', severity: 'error' }); }
      };
      setSnackbar({ open: true, message: `Renamed "${oldName}" → "${newName}"`, severity: 'success', undoAction: undoFn });
      setRenameDialog({ open: false, series: null });
      fetchSeries();
    } catch (err) {
      setSnackbar({ open: true, message: err instanceof Error ? err.message : 'Rename failed', severity: 'error' });
    }
  };

  // Merge
  const openMerge = () => {
    if (selected.size < 2) {
      setSnackbar({ open: true, message: 'Select at least 2 series to merge', severity: 'error' });
      return;
    }
    setMergeKeepId(null);
    setMergeCustomName('');
    setMergeUseCustomName(false);
    setMergeDialog(true);
  };

  const doMerge = async () => {
    if (mergeKeepId === null) return;
    const mergeIds = [...selected].filter((id) => id !== mergeKeepId);
    const finalName = mergeUseCustomName && mergeCustomName.trim() ? mergeCustomName.trim() : undefined;
    const keepName = finalName ?? selectedSeries.find((s) => s.id === mergeKeepId)?.name ?? '?';
    const mergedNames = selectedSeries.filter((s) => s.id !== mergeKeepId).map((s) => s.name).join(', ');
    try {
      await api.mergeSeriesGroup(mergeKeepId, mergeIds, finalName);
      addHistory({ action: 'merge', description: `Merged "${mergedNames}" into "${keepName}"`, undoable: false });
      setSnackbar({ open: true, message: 'Series merged', severity: 'success' });
      setMergeDialog(false);
      setSelected(new Set());
      fetchSeries();
    } catch (err) {
      setSnackbar({ open: true, message: err instanceof Error ? err.message : 'Merge failed', severity: 'error' });
    }
  };

  // Split
  const openSplit = async (s: SeriesType) => {
    setSplitDialog({ open: true, series: s });
    setSplitLoading(true);
    setSplitSelected(new Set());
    try {
      const books = await api.getSeriesBooks(s.id);
      setSplitBooks(books);
    } catch {
      setSplitBooks([]);
    } finally {
      setSplitLoading(false);
    }
  };

  const doSplit = async () => {
    if (!splitDialog.series || splitSelected.size === 0) return;
    const seriesName = splitDialog.series.name;
    try {
      const result = await api.splitSeries(splitDialog.series.id, [...splitSelected]);
      addHistory({ action: 'split', description: `Split ${result.books_moved} book(s) from "${seriesName}"`, undoable: false });
      setSnackbar({ open: true, message: `Split complete: ${result.books_moved} book(s) moved to new series`, severity: 'success' });
      setSplitDialog({ open: false, series: null });
      fetchSeries();
    } catch (err) {
      setSnackbar({ open: true, message: err instanceof Error ? err.message : 'Split failed', severity: 'error' });
    }
  };

  // Delete
  const openDelete = (s: SeriesType) => {
    setDeleteDialog({ open: true, series: s });
  };

  const doDelete = async () => {
    if (!deleteDialog.series) return;
    const seriesName = deleteDialog.series.name;
    try {
      await api.deleteSeries(deleteDialog.series.id);
      addHistory({ action: 'delete', description: `Deleted empty series "${seriesName}"`, undoable: false });
      setSnackbar({ open: true, message: 'Series deleted', severity: 'success' });
      setDeleteDialog({ open: false, series: null });
      fetchSeries();
    } catch (err) {
      setSnackbar({ open: true, message: err instanceof Error ? err.message : 'Delete failed', severity: 'error' });
    }
  };

  // Bulk Delete
  const doBulkDelete = async () => {
    try {
      const result = await api.bulkDeleteSeries([...selected]);
      const msg = result.skipped > 0
        ? `Deleted ${result.deleted}, skipped ${result.skipped} (have books)`
        : `Deleted ${result.deleted} series`;
      addHistory({ action: 'delete', description: msg, undoable: false });
      setSnackbar({ open: true, message: msg, severity: 'success' });
      setBulkDeleteDialog(false);
      setSelected(new Set());
      fetchSeries();
    } catch (err) {
      setSnackbar({ open: true, message: err instanceof Error ? err.message : 'Bulk delete failed', severity: 'error' });
    }
  };

  const selectedSeries = useMemo(
    () => seriesList.filter((s) => selected.has(s.id)),
    [seriesList, selected]
  );

  if (loading && seriesList.length === 0) {
    return (
      <Box sx={{ p: 3, display: 'flex', justifyContent: 'center' }}>
        <CircularProgress />
      </Box>
    );
  }

  const totalCols = visibleColumns.length + 3; // checkbox + expand + actions

  return (
    <Box sx={{ p: 3 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', mb: 2, gap: 2, flexWrap: 'wrap' }}>
        <Typography variant="h5" sx={{ flexGrow: 1 }}>
          Series Management
        </Typography>
        <ColumnPicker
          columns={allColumns}
          isVisible={isColumnVisible}
          onToggle={toggleColumn}
          onReset={resetColumns}
        />
        <Tooltip title="Action History">
          <IconButton onClick={() => setHistoryOpen(true)} disabled={history.length === 0}>
            <Badge badgeContent={history.length} color="primary" max={99}>
              <HistoryIcon />
            </Badge>
          </IconButton>
        </Tooltip>
        <Tooltip title="Refresh">
          <IconButton onClick={fetchSeries} disabled={loading}>
            <RefreshIcon />
          </IconButton>
        </Tooltip>
      </Box>

      {error && (
        <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>
      )}

      {/* Toolbar */}
      <Box sx={{ display: 'flex', gap: 2, mb: 2, flexWrap: 'wrap', alignItems: 'center' }}>
        <TextField
          size="small"
          placeholder="Search series..."
          value={search}
          onChange={(e) => { setSearch(e.target.value); setPage(0); }}
          sx={{ minWidth: 250 }}
        />
        <FormControl size="small" sx={{ minWidth: 160 }}>
          <InputLabel>Filter</InputLabel>
          <Select
            label="Filter"
            value={filter}
            onChange={(e) => { setFilter(e.target.value as SeriesFilter); setPage(0); }}
          >
            <MenuItem value="with_books">With books only</MenuItem>
            <MenuItem value="empty">Empty series</MenuItem>
            <MenuItem value="all">All series</MenuItem>
          </Select>
        </FormControl>
        {selected.size >= 2 && (
          <Button variant="outlined" startIcon={<MergeTypeIcon />} onClick={openMerge}>
            Merge ({selected.size})
          </Button>
        )}
        {selected.size >= 1 && (
          <Button variant="outlined" color="error" startIcon={<DeleteIcon />} onClick={() => setBulkDeleteDialog(true)}>
            Delete ({selected.size})
          </Button>
        )}
      </Box>

      <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
        {filtered.length} series{filter !== 'all' && ` (${seriesList.length} total)`}
      </Typography>

      {/* Table */}
      <TableContainer component={Paper} variant="outlined" sx={{ overflowX: 'auto' }}>
        <Table size="small" sx={{ tableLayout: 'fixed', minWidth: 500 }}>
          <TableHead>
            <TableRow>
              <TableCell padding="checkbox" sx={{ width: 42 }}>
                <Checkbox
                  indeterminate={selected.size > 0 && selected.size < paged.length}
                  checked={paged.length > 0 && selected.size === paged.length}
                  onChange={toggleSelectAll}
                />
              </TableCell>
              <TableCell sx={{ width: 42 }} />
              {visibleColumns.map((col) => (
                <ResizableHeaderCell
                  key={col.key}
                  columnKey={col.key}
                  label={col.label}
                  width={columnWidths[col.key] ?? 150}
                  align={col.align}
                  sortable={col.sortable}
                  sortActive={sortField === col.key}
                  sortDirection={sortField === col.key ? sortDir : 'asc'}
                  onSort={() => handleSort(col.key)}
                  onStartResize={startResize}
                />
              ))}
              <TableCell align="right" sx={{ width: 120 }}>Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {paged.map((s) => (
              <SeriesRow
                key={s.id}
                series={s}
                selected={selected.has(s.id)}
                expanded={expanded[s.id]}
                visibleColumns={visibleColumns}
                columnWidths={columnWidths}
                onToggleSelect={() => toggleSelect(s.id)}
                onToggleExpand={() => toggleExpand(s.id)}
                onRename={() => openRename(s)}
                onSplit={() => openSplit(s)}
                onDelete={() => openDelete(s)}
              />
            ))}
            {paged.length === 0 && (
              <TableRow>
                <TableCell colSpan={totalCols} align="center" sx={{ py: 4 }}>
                  <Typography color="text.secondary">
                    {search ? 'No matching series' : 'No series found'}
                  </Typography>
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </TableContainer>

      <TablePagination
        component="div"
        count={filtered.length}
        page={page}
        onPageChange={(_, p) => setPage(p)}
        rowsPerPage={rowsPerPage}
        onRowsPerPageChange={(e) => { setRowsPerPage(parseInt(e.target.value, 10)); setPage(0); }}
        rowsPerPageOptions={[10, 25, 50, 100]}
      />

      {/* Rename Dialog */}
      <Dialog open={renameDialog.open} onClose={() => setRenameDialog({ open: false, series: null })} maxWidth="sm" fullWidth>
        <DialogTitle>Rename Series</DialogTitle>
        <DialogContent>
          <TextField autoFocus fullWidth label="New name" value={renameValue} onChange={(e) => setRenameValue(e.target.value)} sx={{ mt: 1 }} />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setRenameDialog({ open: false, series: null })}>Cancel</Button>
          <Button variant="contained" onClick={doRename} disabled={!renameValue.trim()}>Rename</Button>
        </DialogActions>
      </Dialog>

      {/* Merge Dialog */}
      <Dialog open={mergeDialog} onClose={() => setMergeDialog(false)} maxWidth="sm" fullWidth>
        <DialogTitle>Merge Series</DialogTitle>
        <DialogContent>
          <Typography variant="body2" sx={{ mb: 2 }}>
            Select which series to keep, then choose to use its existing name or enter a custom name.
          </Typography>
          <Typography variant="subtitle2" sx={{ mb: 1 }}>Keep series:</Typography>
          <RadioGroup value={mergeKeepId ?? ''} onChange={(e) => setMergeKeepId(Number(e.target.value))}>
            {selectedSeries.map((s) => (
              <FormControlLabel
                key={s.id} value={s.id} control={<Radio />}
                label={
                  <Box>
                    <Typography variant="body1" fontWeight={500}>{s.name}</Typography>
                    <Typography variant="body2" color="text.secondary">
                      {s.author_name ? `by ${s.author_name} — ` : ''}{s.book_count ?? 0} book{(s.book_count ?? 0) !== 1 ? 's' : ''} (ID: {s.id})
                    </Typography>
                  </Box>
                }
                sx={{ alignItems: 'flex-start', mb: 1 }}
              />
            ))}
          </RadioGroup>
          {mergeKeepId !== null && (
            <Box sx={{ mt: 2, pl: 1 }}>
              <FormControlLabel
                control={<Checkbox checked={mergeUseCustomName} onChange={(e) => setMergeUseCustomName(e.target.checked)} />}
                label="Use custom name for merged series"
              />
              {mergeUseCustomName && (
                <TextField
                  autoFocus fullWidth size="small"
                  label="Custom series name"
                  value={mergeCustomName}
                  onChange={(e) => setMergeCustomName(e.target.value)}
                  sx={{ mt: 1 }}
                />
              )}
            </Box>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setMergeDialog(false)}>Cancel</Button>
          <Button variant="contained" onClick={doMerge} disabled={mergeKeepId === null || (mergeUseCustomName && !mergeCustomName.trim())}>Merge</Button>
        </DialogActions>
      </Dialog>

      {/* Split Dialog */}
      <Dialog open={splitDialog.open} onClose={() => setSplitDialog({ open: false, series: null })} maxWidth="sm" fullWidth>
        <DialogTitle>Split Series: {splitDialog.series?.name}</DialogTitle>
        <DialogContent>
          <Typography variant="body2" sx={{ mb: 2 }}>Select books to move to a new series.</Typography>
          {splitLoading ? (
            <Box sx={{ display: 'flex', justifyContent: 'center', py: 3 }}><CircularProgress size={24} /></Box>
          ) : (
            <List dense>
              {splitBooks.map((book) => (
                <ListItem key={book.id} dense>
                  <ListItemIcon>
                    <Checkbox
                      edge="start" checked={splitSelected.has(book.id)}
                      onChange={() => {
                        setSplitSelected((prev) => {
                          const next = new Set(prev);
                          if (next.has(book.id)) next.delete(book.id);
                          else next.add(book.id);
                          return next;
                        });
                      }}
                    />
                  </ListItemIcon>
                  <ListItemText primary={book.title} secondary={book.series_position ? `#${book.series_position}` : undefined} />
                </ListItem>
              ))}
              {splitBooks.length === 0 && (
                <Typography color="text.secondary" sx={{ py: 2, textAlign: 'center' }}>No books in this series</Typography>
              )}
            </List>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setSplitDialog({ open: false, series: null })}>Cancel</Button>
          <Button variant="contained" onClick={doSplit} disabled={splitSelected.size === 0 || splitSelected.size === splitBooks.length}>
            Split ({splitSelected.size} book{splitSelected.size !== 1 ? 's' : ''})
          </Button>
        </DialogActions>
      </Dialog>

      {/* Delete Dialog */}
      <Dialog open={deleteDialog.open} onClose={() => setDeleteDialog({ open: false, series: null })}>
        <DialogTitle>Delete Series</DialogTitle>
        <DialogContent>
          <Typography>Are you sure you want to delete &quot;{deleteDialog.series?.name}&quot;?</Typography>
          {(deleteDialog.series?.book_count ?? 0) > 0 && (
            <Alert severity="warning" sx={{ mt: 2 }}>
              This series has {deleteDialog.series?.book_count} book(s). Only empty series can be deleted.
            </Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDeleteDialog({ open: false, series: null })}>Cancel</Button>
          <Button variant="contained" color="error" onClick={doDelete} disabled={(deleteDialog.series?.book_count ?? 0) > 0}>Delete</Button>
        </DialogActions>
      </Dialog>

      {/* Bulk Delete Dialog */}
      <Dialog open={bulkDeleteDialog} onClose={() => setBulkDeleteDialog(false)} maxWidth="sm" fullWidth>
        <DialogTitle>Delete {selected.size} Series</DialogTitle>
        <DialogContent>
          <Typography>This will delete the selected series. Series with books will be skipped.</Typography>
          {selectedSeries.filter((s) => (s.book_count ?? 0) > 0).length > 0 && (
            <Alert severity="info" sx={{ mt: 2 }}>
              {selectedSeries.filter((s) => (s.book_count ?? 0) > 0).length} series have books and will be skipped.
            </Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setBulkDeleteDialog(false)}>Cancel</Button>
          <Button color="error" variant="contained" onClick={doBulkDelete}>
            Delete ({selectedSeries.filter((s) => (s.book_count ?? 0) === 0).length} eligible)
          </Button>
        </DialogActions>
      </Dialog>

      {/* Snackbar with optional Undo */}
      <Snackbar
        open={snackbar.open}
        autoHideDuration={snackbar.undoAction ? 8000 : 4000}
        onClose={() => setSnackbar((prev) => ({ ...prev, open: false }))}
      >
        <Alert
          severity={snackbar.severity}
          onClose={() => setSnackbar((prev) => ({ ...prev, open: false }))}
          action={snackbar.undoAction ? (
            <Button color="inherit" size="small" startIcon={<UndoIcon />} onClick={() => { snackbar.undoAction?.(); setSnackbar((prev) => ({ ...prev, open: false })); }}>
              Undo
            </Button>
          ) : undefined}
        >
          {snackbar.message}
        </Alert>
      </Snackbar>

      {/* History Drawer */}
      <Drawer anchor="right" open={historyOpen} onClose={() => setHistoryOpen(false)}>
        <Box sx={{ width: 380, p: 2 }}>
          <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 2 }}>
            <Typography variant="h6">Action History</Typography>
            {history.length > 0 && <Button size="small" onClick={() => setHistory([])}>Clear</Button>}
          </Box>
          <Divider sx={{ mb: 1 }} />
          {history.length === 0 ? (
            <Typography color="text.secondary" sx={{ py: 4, textAlign: 'center' }}>No actions yet</Typography>
          ) : (
            <List dense>
              {history.map((entry) => (
                <ListItem key={entry.id} secondaryAction={
                  entry.undoable && entry.undoData ? (
                    <Tooltip title="Undo this rename">
                      <IconButton size="small" onClick={async () => {
                        try {
                          await api.renameSeries(entry.undoData!.seriesId, entry.undoData!.oldName);
                          addHistory({ action: 'rename', description: `Undo: reverted rename on "${entry.undoData!.oldName}"`, undoable: false });
                          setSnackbar({ open: true, message: 'Rename undone', severity: 'success' });
                          fetchSeries();
                        } catch { setSnackbar({ open: true, message: 'Undo failed', severity: 'error' }); }
                      }}>
                        <UndoIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                  ) : undefined
                }>
                  <ListItemIcon sx={{ minWidth: 32 }}>
                    {entry.action === 'rename' && <EditIcon fontSize="small" />}
                    {entry.action === 'merge' && <MergeTypeIcon fontSize="small" />}
                    {entry.action === 'split' && <CallSplitIcon fontSize="small" />}
                    {entry.action === 'delete' && <DeleteIcon fontSize="small" />}
                  </ListItemIcon>
                  <ListItemText
                    primary={entry.description}
                    secondary={entry.timestamp.toLocaleTimeString()}
                    primaryTypographyProps={{ variant: 'body2' }}
                  />
                </ListItem>
              ))}
            </List>
          )}
        </Box>
      </Drawer>
    </Box>
  );
}
