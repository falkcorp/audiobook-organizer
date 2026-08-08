// file: web/src/components/MetadataHistory.tsx
// version: 1.3.1
// guid: 8e3a7b2c-5d1f-4a9e-b6c0-2f8d4e7a1b3c

import { useCallback, useEffect, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  IconButton,
  Snackbar,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Tooltip,
  Typography,
} from '@mui/material';
import UndoIcon from '@mui/icons-material/Undo';
import SearchIcon from '@mui/icons-material/Search';
import ArrowRightAltIcon from '@mui/icons-material/ArrowRightAlt';
import RestoreIcon from '@mui/icons-material/Restore';
import DeleteSweepIcon from '@mui/icons-material/DeleteSweep';
import type { MetadataChangeRecord, BookVersionEntry } from '../services/api';
import * as api from '../services/api';

const FIELD_LABELS: Record<string, string> = {
  title: 'Title',
  publisher: 'Publisher',
  language: 'Language',
  audiobook_release_year: 'Release Year',
  cover_url: 'Cover URL',
  author_name: 'Author',
  isbn10: 'ISBN-10',
  isbn13: 'ISBN-13',
  narrator: 'Narrator',
  series_name: 'Series',
  series_position: 'Series Position',
  description: 'Description',
  print_year: 'Print Year',
  isbn: 'ISBN',
  edition: 'Edition',
  work_id: 'Work ID',
};

const CHANGE_TYPE_COLORS: Record<
  string,
  'info' | 'warning' | 'default' | 'success' | 'secondary' | 'primary'
> = {
  fetched: 'info',
  override: 'warning',
  clear: 'default',
  undo: 'success',
  bulk_update: 'secondary',
  search: 'default',
  revert: 'success',
};

function parseJsonValue(raw?: string): string {
  if (raw === undefined || raw === null || raw === '') return '(empty)';
  try {
    const parsed = JSON.parse(raw);
    if (parsed === null || parsed === undefined) return '(empty)';
    return String(parsed);
  } catch {
    return raw;
  }
}

function fieldLabel(field: string): string {
  return FIELD_LABELS[field] || field.replace(/_/g, ' ');
}

interface MetadataHistoryProps {
  bookId: string;
  open: boolean;
  onClose: () => void;
  onUndoComplete?: () => void;
}

export const MetadataHistory = ({
  bookId,
  open,
  onClose,
  onUndoComplete,
}: MetadataHistoryProps) => {
  const [history, setHistory] = useState<MetadataChangeRecord[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [undoingField, setUndoingField] = useState<string | null>(null);
  const [snackbar, setSnackbar] = useState<string | null>(null);
  const [cowVersions, setCowVersions] = useState<BookVersionEntry[]>([]);
  const [cowLoading, setCowLoading] = useState(false);
  const [revertingTimestamp, setRevertingTimestamp] = useState<string | null>(null);
  const [pruning, setPruning] = useState(false);

  const loadCowVersions = useCallback(async () => {
    setCowLoading(true);
    try {
      const versions = await api.getBookCOWVersions(bookId, 50);
      setCowVersions(versions);
    } catch (err) {
      console.error('Failed to load CoW versions', err);
    } finally {
      setCowLoading(false);
    }
  }, [bookId]);

  const loadHistory = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await api.getBookMetadataHistory(bookId);
      setHistory(data);
    } catch (err) {
      console.error('Failed to load metadata history', err);
      setError('Failed to load metadata history.');
    } finally {
      setLoading(false);
    }
  }, [bookId]);

  useEffect(() => {
    if (open) {
      loadHistory();
      loadCowVersions();
    }
  }, [open, loadHistory, loadCowVersions]);

  // Determine the most recent change per field for undo eligibility
  const latestByField = new Set<string>();
  const latestIds = new Set<number>();
  for (const record of history) {
    if (!latestByField.has(record.field)) {
      latestByField.add(record.field);
      latestIds.add(record.id);
    }
  }

  const handleUndo = async (field: string) => {
    setUndoingField(field);
    try {
      const result = await api.undoMetadataChange(bookId, field);
      setSnackbar(result.message || `Undid change to ${fieldLabel(field)}.`);
      await loadHistory();
      onUndoComplete?.();
    } catch (err) {
      console.error('Failed to undo change', err);
      setSnackbar('Failed to undo change.');
    } finally {
      setUndoingField(null);
    }
  };

  const handleRevert = async (timestamp: string) => {
    setRevertingTimestamp(timestamp);
    try {
      const result = await api.revertToSnapshot(bookId, timestamp);
      setSnackbar(result.message || 'Reverted to snapshot.');
      await loadHistory();
      await loadCowVersions();
      onUndoComplete?.();
    } catch (err) {
      console.error('Failed to revert to snapshot', err);
      setSnackbar('Failed to revert to snapshot.');
    } finally {
      setRevertingTimestamp(null);
    }
  };

  const handlePrune = async () => {
    setPruning(true);
    try {
      const result = await api.pruneBookVersions(bookId, 5);
      setSnackbar(`Pruned ${result.pruned} old version(s).`);
      await loadCowVersions();
    } catch (err) {
      console.error('Failed to prune versions', err);
      setSnackbar('Failed to prune versions.');
    } finally {
      setPruning(false);
    }
  };

  return (
    <>
      <Dialog open={open} onClose={onClose} maxWidth="md" fullWidth>
        <DialogTitle>Metadata Change History</DialogTitle>
        <DialogContent dividers>
          {loading && (
            <Box display="flex" justifyContent="center" py={3}>
              <CircularProgress />
            </Box>
          )}
          {error && (
            <Alert severity="error" sx={{ mb: 2 }}>
              {error}
            </Alert>
          )}
          {!loading && !error && history.length === 0 && (
            <Alert severity="info">No metadata changes recorded yet.</Alert>
          )}
          {!loading && history.length > 0 && (
            <Table size="small">
              <TableHead>
                <TableRow>
                  <TableCell>Field</TableCell>
                  <TableCell>Change</TableCell>
                  <TableCell>Type</TableCell>
                  <TableCell>Source</TableCell>
                  <TableCell>When</TableCell>
                  <TableCell align="right">Undo</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {history.map((record) => {
                  const isSearch = record.field === '__search__';
                  return (
                  <TableRow key={record.id} sx={isSearch ? { bgcolor: 'action.hover' } : undefined}>
                    <TableCell>
                      <Typography variant="body2" fontWeight={500}>
                        {isSearch ? (
                          <Stack direction="row" spacing={0.5} alignItems="center">
                            <SearchIcon fontSize="small" color="action" />
                            <span>Search</span>
                          </Stack>
                        ) : (
                          fieldLabel(record.field)
                        )}
                      </Typography>
                    </TableCell>
                    <TableCell>
                      {isSearch ? (
                        <Typography variant="body2" color="text.secondary">
                          {parseJsonValue(record.new_value)}
                        </Typography>
                      ) : (
                      <Stack
                        direction="row"
                        spacing={0.5}
                        alignItems="center"
                        flexWrap="wrap"
                      >
                        <Typography
                          variant="body2"
                          color="text.secondary"
                          sx={{
                            maxWidth: 120,
                            overflow: 'hidden',
                            textOverflow: 'ellipsis',
                            whiteSpace: 'nowrap',
                          }}
                        >
                          {parseJsonValue(record.previous_value)}
                        </Typography>
                        <ArrowRightAltIcon
                          fontSize="small"
                          color="action"
                        />
                        <Typography
                          variant="body2"
                          sx={{
                            maxWidth: 120,
                            overflow: 'hidden',
                            textOverflow: 'ellipsis',
                            whiteSpace: 'nowrap',
                          }}
                        >
                          {parseJsonValue(record.new_value)}
                        </Typography>
                      </Stack>
                      )}
                    </TableCell>
                    <TableCell>
                      <Chip
                        label={record.change_type}
                        size="small"
                        color={
                          CHANGE_TYPE_COLORS[record.change_type] || 'default'
                        }
                      />
                    </TableCell>
                    <TableCell>
                      <Typography variant="body2" color="text.secondary">
                        {record.source || '—'}
                      </Typography>
                    </TableCell>
                    <TableCell>
                      <Typography variant="body2" color="text.secondary">
                        {new Date(record.changed_at).toLocaleString()}
                      </Typography>
                    </TableCell>
                    <TableCell align="right">
                      {!isSearch && latestIds.has(record.id) &&
                      record.change_type !== 'undo' ? (
                        <Tooltip title={`Undo this ${fieldLabel(record.field)} change`}>
                          <span>
                            <IconButton
                              size="small"
                              onClick={() => handleUndo(record.field)}
                              disabled={undoingField === record.field}
                            >
                              {undoingField === record.field ? (
                                <CircularProgress size={18} />
                              ) : (
                                <UndoIcon fontSize="small" />
                              )}
                            </IconButton>
                          </span>
                        </Tooltip>
                      ) : null}
                    </TableCell>
                  </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
          {/* Version Snapshots Section */}
          <Box sx={{ mt: 3 }}>
            <Stack direction="row" alignItems="center" spacing={1} mb={1}>
              <RestoreIcon fontSize="small" />
              <Typography variant="h6">Version Snapshots</Typography>
              {cowVersions.length > 5 && (
                <Button
                  size="small"
                  variant="outlined"
                  startIcon={pruning ? <CircularProgress size={14} /> : <DeleteSweepIcon />}
                  onClick={handlePrune}
                  disabled={pruning}
                >
                  {pruning ? 'Pruning...' : 'Prune (keep 5)'}
                </Button>
              )}
            </Stack>
            {cowLoading && (
              <Box display="flex" justifyContent="center" py={2}>
                <CircularProgress size={24} />
              </Box>
            )}
            {!cowLoading && cowVersions.length === 0 && (
              <Alert severity="info" sx={{ mb: 1 }}>
                No version snapshots recorded yet. Snapshots are created automatically when metadata is updated.
              </Alert>
            )}
            {!cowLoading && cowVersions.length > 0 && (
              <Table size="small">
                <TableHead>
                  <TableRow>
                    <TableCell>Snapshot Time</TableCell>
                    <TableCell align="right">Actions</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {cowVersions.map((version, idx) => (
                    <TableRow key={version.timestamp}>
                      <TableCell>
                        <Typography variant="body2">
                          {new Date(version.timestamp).toLocaleString()}
                          {idx === 0 && (
                            <Chip label="latest" size="small" color="primary" variant="outlined" sx={{ ml: 1 }} />
                          )}
                        </Typography>
                      </TableCell>
                      <TableCell align="right">
                        <Tooltip title="Revert metadata to this snapshot">
                          <span>
                            <Button
                              size="small"
                              variant="outlined"
                              startIcon={
                                revertingTimestamp === version.timestamp
                                  ? <CircularProgress size={14} />
                                  : <RestoreIcon />
                              }
                              onClick={() => handleRevert(version.timestamp)}
                              disabled={revertingTimestamp !== null || idx === 0}
                            >
                              {revertingTimestamp === version.timestamp ? 'Reverting...' : 'Revert'}
                            </Button>
                          </span>
                        </Tooltip>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </Box>
        </DialogContent>
        <DialogActions>
          <Button onClick={onClose}>Close</Button>
        </DialogActions>
      </Dialog>
      <Snackbar
        open={!!snackbar}
        autoHideDuration={4000}
        onClose={() => setSnackbar(null)}
        message={snackbar}
      />
    </>
  );
};
