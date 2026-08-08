// file: web/src/pages/Authors.tsx
// version: 1.3.1
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Alert,
  Badge,
  Box,
  Button,
  Checkbox,
  Chip,
  CircularProgress,
  Collapse,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  Divider,
  Drawer,
  FormControl,
  IconButton,
  InputLabel,
  List,
  ListItem,
  ListItemIcon,
  ListItemText,
  MenuItem,
  Paper,
  Select,
  Snackbar,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TablePagination,
  TableRow,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import KeyboardArrowDownIcon from '@mui/icons-material/KeyboardArrowDown';
import KeyboardArrowUpIcon from '@mui/icons-material/KeyboardArrowUp';
import DeleteIcon from '@mui/icons-material/Delete';
import EditIcon from '@mui/icons-material/Edit';
import MergeTypeIcon from '@mui/icons-material/MergeType';
import CallSplitIcon from '@mui/icons-material/CallSplit';
import LabelIcon from '@mui/icons-material/Label';
import RefreshIcon from '@mui/icons-material/Refresh';
import HistoryIcon from '@mui/icons-material/History';
import UndoIcon from '@mui/icons-material/Undo';
import {
  useConfigurableTable,
  ResizableHeaderCell,
  ColumnPicker,
  type ColumnDef,
} from '../components/common/ConfigurableTable';
import * as api from '../services/api';

interface ActionHistoryEntry {
  id: string;
  action: 'rename' | 'merge' | 'split' | 'delete' | 'alias';
  description: string;
  timestamp: Date;
  undoable: boolean;
  undoData?: { authorId: number; oldName: string };
}

type AuthorFilter = 'with_books' | 'zero_books' | 'all';

// --- Column definitions ---

const authorColumns: ColumnDef<api.AuthorWithCount>[] = [
  {
    key: 'name',
    label: 'Author',
    defaultVisible: true,
    defaultWidth: 300,
    minWidth: 120,
    sortable: true,
    sortValue: (a) => a.name.toLowerCase(),
    render: (a) => (
      <Box>
        <Typography variant="body2" fontWeight={500}>{a.name}</Typography>
        {a.aliases.length > 0 && (
          <Box sx={{ mt: 0.5, display: 'flex', flexWrap: 'wrap', gap: 0.5 }}>
            {a.aliases.map((alias) => (
              <Chip key={alias.id} label={alias.alias_name} size="small" variant="outlined" color="secondary" title={alias.alias_type} />
            ))}
          </Box>
        )}
      </Box>
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
    sortValue: (a) => a.book_count,
    render: (a) => a.file_count > a.book_count
      ? `${a.book_count} (${a.file_count} files)`
      : `${a.book_count}`,
  },
  {
    key: 'alias_count',
    label: 'Aliases',
    defaultVisible: false,
    defaultWidth: 80,
    minWidth: 60,
    align: 'right',
    sortable: true,
    sortValue: (a) => a.aliases.length,
    render: (a) => a.aliases.length || '-',
  },
  {
    key: 'id',
    label: 'ID',
    defaultVisible: false,
    defaultWidth: 70,
    minWidth: 50,
    align: 'right',
    sortable: true,
    sortValue: (a) => a.id,
    render: (a) => a.id,
  },
];

// --- AuthorRow ---

interface AuthorRowProps {
  author: api.AuthorWithCount;
  selected: boolean;
  visibleColumns: ColumnDef<api.AuthorWithCount>[];
  columnWidths: Record<string, number>;
  onSelect: (id: number) => void;
  onRename: (author: api.AuthorWithCount) => void;
  onSplit: (author: api.AuthorWithCount) => void;
  onDelete: (author: api.AuthorWithCount) => void;
  onManageAliases: (author: api.AuthorWithCount) => void;
}

function AuthorRow({ author, selected, visibleColumns, columnWidths, onSelect, onRename, onSplit, onDelete, onManageAliases }: AuthorRowProps) {
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [books, setBooks] = useState<api.Book[]>([]);
  const [loadingBooks, setLoadingBooks] = useState(false);

  const handleExpand = useCallback(async () => {
    if (!open && books.length === 0) {
      setLoadingBooks(true);
      try {
        const data = await api.getAuthorBooks(author.id);
        setBooks(data);
      } catch {
        // silently fail
      } finally {
        setLoadingBooks(false);
      }
    }
    setOpen(!open);
  }, [open, books.length, author.id]);

  const totalCols = visibleColumns.length + 3; // checkbox + expand + actions

  return (
    <>
      <TableRow hover sx={{ cursor: 'pointer' }} onClick={(e) => { if (!(e.target as HTMLElement).closest('button, input, .MuiCheckbox-root')) void handleExpand(); }}>
        <TableCell padding="checkbox" sx={{ width: 42 }}>
          <Checkbox checked={selected} onChange={() => onSelect(author.id)} />
        </TableCell>
        <TableCell sx={{ width: 42 }}>
          <IconButton size="small" onClick={(e) => { e.stopPropagation(); void handleExpand(); }}>
            {open ? <KeyboardArrowUpIcon /> : <KeyboardArrowDownIcon />}
          </IconButton>
        </TableCell>
        {visibleColumns.map((col) => (
          <TableCell
            key={col.key}
            align={col.align}
            sx={{ width: columnWidths[col.key], maxWidth: columnWidths[col.key], overflow: 'hidden', textOverflow: 'ellipsis' }}
          >
            {col.render(author)}
          </TableCell>
        ))}
        <TableCell align="right" sx={{ width: 160, whiteSpace: 'nowrap' }} onClick={(e) => e.stopPropagation()}>
          <Tooltip title="Rename">
            <IconButton size="small" onClick={() => onRename(author)}><EditIcon fontSize="small" /></IconButton>
          </Tooltip>
          <Tooltip title="Manage Aliases">
            <IconButton size="small" onClick={() => onManageAliases(author)}><LabelIcon fontSize="small" /></IconButton>
          </Tooltip>
          <Tooltip title="Split Composite">
            <IconButton size="small" onClick={() => onSplit(author)}><CallSplitIcon fontSize="small" /></IconButton>
          </Tooltip>
          {author.book_count === 0 && (
            <Tooltip title="Delete">
              <IconButton size="small" color="error" onClick={() => onDelete(author)}><DeleteIcon fontSize="small" /></IconButton>
            </Tooltip>
          )}
        </TableCell>
      </TableRow>
      <TableRow>
        <TableCell colSpan={totalCols} sx={{ py: 0, borderBottom: open ? undefined : 'none' }}>
          <Collapse in={open} timeout="auto" unmountOnExit>
            <Box sx={{ py: 1, pl: 4 }}>
              {loadingBooks ? (
                <CircularProgress size={20} />
              ) : books.length === 0 ? (
                <Typography variant="body2" color="text.secondary">No books found.</Typography>
              ) : (
                <Table size="small">
                  <TableHead>
                    <TableRow>
                      <TableCell>Title</TableCell>
                      <TableCell>Series</TableCell>
                      <TableCell>Format</TableCell>
                      <TableCell>Duration</TableCell>
                      <TableCell>File Path</TableCell>
                    </TableRow>
                  </TableHead>
                  <TableBody>
                    {books.map((book) => (
                      <TableRow key={book.id} hover sx={{ cursor: 'pointer' }} onClick={() => navigate(`/library/${book.id}`)}>
                        <TableCell>
                          <Typography variant="body2" color="primary" sx={{ '&:hover': { textDecoration: 'underline' } }}>
                            {book.title}
                          </Typography>
                        </TableCell>
                        <TableCell>
                          <Typography variant="body2" color="text.secondary" noWrap>{book.series_name ?? '-'}</Typography>
                        </TableCell>
                        <TableCell>{book.format || '-'}</TableCell>
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
    </>
  );
}

// --- Main Component ---

export function Authors() {
  const [authors, setAuthors] = useState<api.AuthorWithCount[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [filter, setFilter] = useState<AuthorFilter>('with_books');
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [page, setPage] = useState(0);
  const [rowsPerPage, setRowsPerPage] = useState(25);

  // Dialog state
  const [renameDialog, setRenameDialog] = useState<api.AuthorWithCount | null>(null);
  const [renameValue, setRenameValue] = useState('');
  const [deleteDialog, setDeleteDialog] = useState<api.AuthorWithCount | null>(null);
  const [mergeDialog, setMergeDialog] = useState(false);
  const [mergeCanonical, setMergeCanonical] = useState<number | ''>('');
  const [splitDialog, setSplitDialog] = useState<api.AuthorWithCount | null>(null);
  const [splitNames, setSplitNames] = useState('');
  const [aliasDialog, setAliasDialog] = useState<api.AuthorWithCount | null>(null);
  const [aliases, setAliases] = useState<api.AuthorAlias[]>([]);
  const [newAliasName, setNewAliasName] = useState('');
  const [newAliasType, setNewAliasType] = useState('alias');
  const [actionLoading, setActionLoading] = useState(false);
  const [snackbar, setSnackbar] = useState<{ open: boolean; message: string; severity: 'success' | 'error'; undoAction?: () => void }>({ open: false, message: '', severity: 'success' });
  const [history, setHistory] = useState<ActionHistoryEntry[]>([]);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [bulkDeleteDialog, setBulkDeleteDialog] = useState(false);

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
  } = useConfigurableTable<api.AuthorWithCount>({
    storageKey: 'authors',
    columns: authorColumns,
    defaultSortField: 'name',
    defaultSortDir: 'asc',
  });

  const addHistory = (entry: Omit<ActionHistoryEntry, 'id' | 'timestamp'>) => {
    setHistory((prev) => [{ ...entry, id: crypto.randomUUID(), timestamp: new Date() }, ...prev].slice(0, 50));
  };

  const loadAuthors = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await api.getAuthorsWithCounts();
      setAuthors(data);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load authors');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadAuthors();
  }, [loadAuthors]);

  const filtered = useMemo(() => {
    let list = authors;
    if (filter === 'with_books') {
      list = list.filter((a) => a.book_count > 0);
    } else if (filter === 'zero_books') {
      list = list.filter((a) => a.book_count === 0);
    }
    if (search.trim()) {
      const q = search.toLowerCase();
      list = list.filter(
        (a) =>
          a.name.toLowerCase().includes(q) ||
          a.aliases.some((al) => al.alias_name.toLowerCase().includes(q))
      );
    }
    return sortRows(list);
  }, [authors, search, filter, sortRows]);

  const paged = useMemo(
    () => filtered.slice(page * rowsPerPage, page * rowsPerPage + rowsPerPage),
    [filtered, page, rowsPerPage]
  );

  const handleSelect = (id: number) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const handleSelectAll = () => {
    if (selected.size === paged.length) {
      setSelected(new Set());
    } else {
      setSelected(new Set(paged.map((a) => a.id)));
    }
  };

  // Rename
  const handleOpenRename = (author: api.AuthorWithCount) => {
    setRenameDialog(author);
    setRenameValue(author.name);
  };
  const handleRename = async () => {
    if (!renameDialog || !renameValue.trim()) return;
    const oldName = renameDialog.name;
    const authorId = renameDialog.id;
    const newName = renameValue.trim();
    setActionLoading(true);
    try {
      await api.renameAuthor(authorId, newName);
      addHistory({ action: 'rename', description: `Renamed "${oldName}" → "${newName}"`, undoable: true, undoData: { authorId, oldName } });
      const undoFn = async () => {
        try {
          await api.renameAuthor(authorId, oldName);
          addHistory({ action: 'rename', description: `Undo: renamed "${newName}" back to "${oldName}"`, undoable: false });
          setSnackbar({ open: true, message: 'Rename undone', severity: 'success' });
          await loadAuthors();
        } catch { setSnackbar({ open: true, message: 'Undo failed', severity: 'error' }); }
      };
      setSnackbar({ open: true, message: `Renamed "${oldName}" → "${newName}"`, severity: 'success', undoAction: undoFn });
      setRenameDialog(null);
      await loadAuthors();
    } catch (err) {
      setSnackbar({ open: true, message: err instanceof Error ? err.message : 'Rename failed', severity: 'error' });
    } finally {
      setActionLoading(false);
    }
  };

  // Delete
  const handleDelete = async () => {
    if (!deleteDialog) return;
    setActionLoading(true);
    try {
      await api.deleteAuthor(deleteDialog.id);
      addHistory({ action: 'delete', description: `Deleted "${deleteDialog.name}"`, undoable: false });
      setSnackbar({ open: true, message: `Deleted "${deleteDialog.name}"`, severity: 'success' });
      setDeleteDialog(null);
      await loadAuthors();
    } catch (err) {
      setSnackbar({ open: true, message: err instanceof Error ? err.message : 'Delete failed', severity: 'error' });
    } finally {
      setActionLoading(false);
    }
  };

  // Merge
  const handleOpenMerge = () => {
    if (selected.size < 2) return;
    setMergeCanonical('');
    setMergeDialog(true);
  };
  const handleMerge = async () => {
    if (typeof mergeCanonical !== 'number') return;
    const mergeIds = [...selected].filter((id) => id !== mergeCanonical);
    if (mergeIds.length === 0) return;
    setActionLoading(true);
    try {
      await api.mergeAuthors(mergeCanonical, mergeIds);
      const keepName = selectedAuthors.find((a) => a.id === mergeCanonical)?.name ?? '?';
      const mergedNames = selectedAuthors.filter((a) => a.id !== mergeCanonical).map((a) => a.name).join(', ');
      addHistory({ action: 'merge', description: `Merged "${mergedNames}" into "${keepName}"`, undoable: false });
      setSnackbar({ open: true, message: `Merged ${mergeIds.length} author(s)`, severity: 'success' });
      setMergeDialog(false);
      setSelected(new Set());
      await loadAuthors();
    } catch (err) {
      setSnackbar({ open: true, message: err instanceof Error ? err.message : 'Merge failed', severity: 'error' });
    } finally {
      setActionLoading(false);
    }
  };

  // Bulk Delete
  const handleBulkDelete = async () => {
    setActionLoading(true);
    try {
      const result = await api.bulkDeleteAuthors([...selected]);
      const msg = result.skipped > 0
        ? `Deleted ${result.deleted}, skipped ${result.skipped} (have books)`
        : `Deleted ${result.deleted} author(s)`;
      addHistory({ action: 'delete', description: msg, undoable: false });
      setSnackbar({ open: true, message: msg, severity: 'success' });
      setBulkDeleteDialog(false);
      setSelected(new Set());
      await loadAuthors();
    } catch (err) {
      setSnackbar({ open: true, message: err instanceof Error ? err.message : 'Bulk delete failed', severity: 'error' });
    } finally {
      setActionLoading(false);
    }
  };

  // Split
  const handleOpenSplit = (author: api.AuthorWithCount) => {
    setSplitDialog(author);
    setSplitNames('');
  };
  const handleSplit = async () => {
    if (!splitDialog) return;
    setActionLoading(true);
    try {
      const names = splitNames.trim()
        ? splitNames.split(',').map((n) => n.trim()).filter(Boolean)
        : undefined;
      await api.splitCompositeAuthor(splitDialog.id, names);
      addHistory({ action: 'split', description: `Split "${splitDialog.name}"`, undoable: false });
      setSnackbar({ open: true, message: `Split "${splitDialog.name}"`, severity: 'success' });
      setSplitDialog(null);
      await loadAuthors();
    } catch (err) {
      setSnackbar({ open: true, message: err instanceof Error ? err.message : 'Split failed', severity: 'error' });
    } finally {
      setActionLoading(false);
    }
  };

  // Alias management
  const handleOpenAliases = async (author: api.AuthorWithCount) => {
    setAliasDialog(author);
    setAliases(author.aliases);
    setNewAliasName('');
    setNewAliasType('alias');
  };
  const handleAddAlias = async () => {
    if (!aliasDialog || !newAliasName.trim()) return;
    setActionLoading(true);
    try {
      const created = await api.createAuthorAlias(aliasDialog.id, newAliasName.trim(), newAliasType);
      setAliases((prev) => [...prev, created]);
      setNewAliasName('');
      addHistory({ action: 'alias', description: `Added alias "${created.alias_name}" to "${aliasDialog.name}"`, undoable: false });
      setSnackbar({ open: true, message: `Added alias "${created.alias_name}"`, severity: 'success' });
    } catch (err) {
      setSnackbar({ open: true, message: err instanceof Error ? err.message : 'Failed to add alias', severity: 'error' });
    } finally {
      setActionLoading(false);
    }
  };
  const handleRemoveAlias = async (aliasId: number) => {
    if (!aliasDialog) return;
    setActionLoading(true);
    try {
      await api.deleteAuthorAlias(aliasDialog.id, aliasId);
      setAliases((prev) => prev.filter((a) => a.id !== aliasId));
      setSnackbar({ open: true, message: 'Alias removed', severity: 'success' });
    } catch (err) {
      setSnackbar({ open: true, message: err instanceof Error ? err.message : 'Failed to remove alias', severity: 'error' });
    } finally {
      setActionLoading(false);
    }
  };

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
        <Typography variant="h4">Authors</Typography>
        <Alert
          severity="error"
          action={<Button color="inherit" size="small" onClick={() => void loadAuthors()}>Retry</Button>}
        >
          {error}
        </Alert>
      </Stack>
    );
  }

  const selectedAuthors = authors.filter((a) => selected.has(a.id));
  const totalCols = visibleColumns.length + 3; // checkbox + expand + actions

  return (
    <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      <Typography variant="h4">Authors</Typography>
      <Typography variant="body2" color="text.secondary">
        Manage authors, aliases, and merge duplicates. {authors.length} total authors.
      </Typography>

      {/* Toolbar */}
      <Stack direction="row" spacing={2} alignItems="center" flexWrap="wrap">
        <TextField
          size="small"
          placeholder="Search authors..."
          value={search}
          onChange={(e) => { setSearch(e.target.value); setPage(0); }}
          sx={{ minWidth: 250 }}
        />
        <FormControl size="small" sx={{ minWidth: 160 }}>
          <InputLabel>Filter</InputLabel>
          <Select
            label="Filter"
            value={filter}
            onChange={(e) => { setFilter(e.target.value as AuthorFilter); setPage(0); }}
          >
            <MenuItem value="with_books">With books only</MenuItem>
            <MenuItem value="zero_books">Zero-book authors</MenuItem>
            <MenuItem value="all">All authors</MenuItem>
          </Select>
        </FormControl>
        <ColumnPicker
          columns={allColumns}
          isVisible={isColumnVisible}
          onToggle={toggleColumn}
          onReset={resetColumns}
        />
        <Tooltip title="Action History">
          <span>
            <IconButton onClick={() => setHistoryOpen(true)} disabled={history.length === 0}>
              <Badge badgeContent={history.length} color="primary" max={99}>
                <HistoryIcon />
              </Badge>
            </IconButton>
          </span>
        </Tooltip>
        <Button startIcon={<RefreshIcon />} onClick={() => void loadAuthors()} size="small">
          Refresh
        </Button>
        {selected.size >= 2 && (
          <Button startIcon={<MergeTypeIcon />} variant="contained" size="small" onClick={handleOpenMerge}>
            Merge ({selected.size})
          </Button>
        )}
        {selected.size >= 1 && (
          <Button
            startIcon={<DeleteIcon />}
            variant="outlined"
            color="error"
            size="small"
            onClick={() => setBulkDeleteDialog(true)}
          >
            Delete ({selected.size})
          </Button>
        )}
      </Stack>

      <Typography variant="body2" color="text.secondary">
        {filtered.length} author{filtered.length !== 1 ? 's' : ''} shown
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
                  onChange={handleSelectAll}
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
              <TableCell align="right" sx={{ width: 160 }}>Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {paged.length === 0 ? (
              <TableRow>
                <TableCell colSpan={totalCols} align="center">
                  <Typography variant="body2" color="text.secondary" sx={{ py: 4 }}>
                    {search ? 'No authors match your search.' : 'No authors found.'}
                  </Typography>
                </TableCell>
              </TableRow>
            ) : (
              paged.map((author) => (
                <AuthorRow
                  key={author.id}
                  author={author}
                  selected={selected.has(author.id)}
                  visibleColumns={visibleColumns}
                  columnWidths={columnWidths}
                  onSelect={handleSelect}
                  onRename={handleOpenRename}
                  onSplit={handleOpenSplit}
                  onDelete={(a) => setDeleteDialog(a)}
                  onManageAliases={handleOpenAliases}
                />
              ))
            )}
          </TableBody>
        </Table>
        <TablePagination
          component="div"
          count={filtered.length}
          page={page}
          onPageChange={(_, p) => setPage(p)}
          rowsPerPage={rowsPerPage}
          onRowsPerPageChange={(e) => { setRowsPerPage(parseInt(e.target.value, 10)); setPage(0); }}
          rowsPerPageOptions={[10, 25, 50, 100]}
        />
      </TableContainer>

      {/* Rename Dialog */}
      <Dialog open={!!renameDialog} onClose={() => setRenameDialog(null)} maxWidth="sm" fullWidth>
        <DialogTitle>Rename Author</DialogTitle>
        <DialogContent>
          <TextField
            autoFocus fullWidth label="New Name" value={renameValue}
            onChange={(e) => setRenameValue(e.target.value)} sx={{ mt: 1 }}
            onKeyDown={(e) => { if (e.key === 'Enter') void handleRename(); }}
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setRenameDialog(null)}>Cancel</Button>
          <Button onClick={() => void handleRename()} variant="contained" disabled={actionLoading || !renameValue.trim()}>
            {actionLoading ? <CircularProgress size={20} /> : 'Rename'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Delete Confirmation Dialog */}
      <Dialog open={!!deleteDialog} onClose={() => setDeleteDialog(null)}>
        <DialogTitle>Delete Author</DialogTitle>
        <DialogContent>
          <DialogContentText>
            Are you sure you want to delete &quot;{deleteDialog?.name}&quot;? This action cannot be undone.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDeleteDialog(null)}>Cancel</Button>
          <Button onClick={() => void handleDelete()} color="error" variant="contained" disabled={actionLoading}>
            {actionLoading ? <CircularProgress size={20} /> : 'Delete'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Merge Dialog */}
      <Dialog open={mergeDialog} onClose={() => setMergeDialog(false)} maxWidth="sm" fullWidth>
        <DialogTitle>Merge Authors</DialogTitle>
        <DialogContent>
          <DialogContentText sx={{ mb: 2 }}>
            Select the canonical (primary) author. All other selected authors will be merged into it.
          </DialogContentText>
          <FormControl fullWidth>
            <InputLabel>Canonical Author</InputLabel>
            <Select value={mergeCanonical} label="Canonical Author" onChange={(e) => setMergeCanonical(e.target.value as number)}>
              {selectedAuthors.map((a) => (
                <MenuItem key={a.id} value={a.id}>{a.name} ({a.book_count} books)</MenuItem>
              ))}
            </Select>
          </FormControl>
          {typeof mergeCanonical === 'number' && (
            <Box sx={{ mt: 2 }}>
              <Typography variant="body2" color="text.secondary">
                Will merge into &quot;{selectedAuthors.find((a) => a.id === mergeCanonical)?.name}&quot;:
              </Typography>
              {selectedAuthors.filter((a) => a.id !== mergeCanonical).map((a) => (
                <Chip key={a.id} label={a.name} sx={{ mr: 0.5, mt: 0.5 }} />
              ))}
            </Box>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setMergeDialog(false)}>Cancel</Button>
          <Button onClick={() => void handleMerge()} variant="contained" disabled={actionLoading || typeof mergeCanonical !== 'number'}>
            {actionLoading ? <CircularProgress size={20} /> : 'Merge'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Bulk Delete Dialog */}
      <Dialog open={bulkDeleteDialog} onClose={() => setBulkDeleteDialog(false)} maxWidth="sm" fullWidth>
        <DialogTitle>Delete {selected.size} Author(s)</DialogTitle>
        <DialogContent>
          <DialogContentText>
            This will delete the selected authors. Authors with books will be skipped.
          </DialogContentText>
          {selectedAuthors.filter((a) => a.book_count > 0).length > 0 && (
            <Alert severity="info" sx={{ mt: 2 }}>
              {selectedAuthors.filter((a) => a.book_count > 0).length} author(s) have books and will be skipped.
            </Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setBulkDeleteDialog(false)}>Cancel</Button>
          <Button onClick={() => void handleBulkDelete()} color="error" variant="contained" disabled={actionLoading}>
            {actionLoading ? <CircularProgress size={20} /> : `Delete (${selectedAuthors.filter((a) => a.book_count === 0).length} eligible)`}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Split Dialog */}
      <Dialog open={!!splitDialog} onClose={() => setSplitDialog(null)} maxWidth="sm" fullWidth>
        <DialogTitle>Split Composite Author</DialogTitle>
        <DialogContent>
          <DialogContentText sx={{ mb: 2 }}>
            Split &quot;{splitDialog?.name}&quot; into separate authors. Leave blank for automatic splitting, or enter comma-separated names.
          </DialogContentText>
          <TextField
            fullWidth label="Names (comma-separated, optional)" value={splitNames}
            onChange={(e) => setSplitNames(e.target.value)} placeholder="e.g. John Smith, Jane Doe"
            onKeyDown={(e) => { if (e.key === 'Enter') void handleSplit(); }}
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setSplitDialog(null)}>Cancel</Button>
          <Button onClick={() => void handleSplit()} variant="contained" disabled={actionLoading}>
            {actionLoading ? <CircularProgress size={20} /> : 'Split'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Aliases Dialog */}
      <Dialog open={!!aliasDialog} onClose={() => { setAliasDialog(null); void loadAuthors(); }} maxWidth="sm" fullWidth>
        <DialogTitle>Manage Aliases for &quot;{aliasDialog?.name}&quot;</DialogTitle>
        <DialogContent>
          {aliases.length === 0 ? (
            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>No aliases yet.</Typography>
          ) : (
            <Box sx={{ mb: 2, display: 'flex', flexWrap: 'wrap', gap: 0.5 }}>
              {aliases.map((alias) => (
                <Chip key={alias.id} label={`${alias.alias_name} (${alias.alias_type})`} onDelete={() => void handleRemoveAlias(alias.id)} />
              ))}
            </Box>
          )}
          <Stack direction="row" spacing={1} alignItems="flex-end">
            <TextField
              size="small" label="Alias Name" value={newAliasName}
              onChange={(e) => setNewAliasName(e.target.value)} sx={{ flex: 1 }}
              onKeyDown={(e) => { if (e.key === 'Enter') void handleAddAlias(); }}
            />
            <FormControl size="small" sx={{ minWidth: 120 }}>
              <InputLabel>Type</InputLabel>
              <Select value={newAliasType} label="Type" onChange={(e) => setNewAliasType(e.target.value)}>
                <MenuItem value="alias">Alias</MenuItem>
                <MenuItem value="pen_name">Pen Name</MenuItem>
                <MenuItem value="real_name">Real Name</MenuItem>
                <MenuItem value="abbreviation">Abbreviation</MenuItem>
                <MenuItem value="handle">Handle</MenuItem>
              </Select>
            </FormControl>
            <Button variant="outlined" size="small" onClick={() => void handleAddAlias()} disabled={actionLoading || !newAliasName.trim()}>
              Add
            </Button>
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => { setAliasDialog(null); void loadAuthors(); }}>Close</Button>
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
                          await api.renameAuthor(entry.undoData!.authorId, entry.undoData!.oldName);
                          addHistory({ action: 'rename', description: `Undo: reverted rename on "${entry.undoData!.oldName}"`, undoable: false });
                          setSnackbar({ open: true, message: 'Rename undone', severity: 'success' });
                          await loadAuthors();
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
                    {entry.action === 'alias' && <LabelIcon fontSize="small" />}
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
