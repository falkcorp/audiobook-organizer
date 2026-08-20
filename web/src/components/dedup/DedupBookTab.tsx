// file: web/src/components/dedup/DedupBookTab.tsx
// version: 1.0.1
// guid: 71F51230-1BB6-4864-A1EB-120EE776D673
// last-edited: 2026-08-19

import { useState, useEffect, useCallback } from 'react';
import {
  Box,
  Typography,
  Paper,
  Button,
  Alert,
  Chip,
  CircularProgress,
  IconButton,
  Tooltip,
  Card,
  CardContent,
  CardActions,
  Stack,
  Radio,
  RadioGroup,
  FormControlLabel,
  Checkbox,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogContentText,
  DialogActions,
  Divider,
} from '@mui/material';
import MergeIcon from '@mui/icons-material/MergeType';
import RefreshIcon from '@mui/icons-material/Refresh';
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import FolderIcon from '@mui/icons-material/Folder';
import * as api from '../../services/api';
import type { Book, Operation } from '../../services/api';
import {
  cleanDisplayTitle,
  OperationProgress,
  usePagination,
  PaginationControls,
  runOperationWithPolling,
} from './dedupHelpers';

export function DedupBookTab() {
  const [groups, setGroups] = useState<Book[][]>([]);
  const [totalDuplicates, setTotalDuplicates] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [activeOp, setActiveOp] = useState<Operation | null>(null);
  const [mergeSuccess, setMergeSuccess] = useState<string | null>(null);
  const [keepSelections, setKeepSelections] = useState<Record<string, string>>({});
  const [selectedGroups, setSelectedGroups] = useState<Set<string>>(new Set());
  const [confirmOpen, setConfirmOpen] = useState(false);
  const pagination = usePagination(groups.length);

  const fetchDuplicates = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await api.getBookDuplicates();
      setGroups(data.groups || []);
      setTotalDuplicates(data.duplicate_count || 0);
      const defaults: Record<string, string> = {};
      (data.groups || []).forEach((g, i) => {
        if (g.length > 0) defaults[`group-${i}`] = g[0].id;
      });
      setKeepSelections(defaults);
      setSelectedGroups(new Set());
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to fetch duplicates');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchDuplicates();
  }, [fetchDuplicates]);

  const handleMerge = async (group: Book[], groupKey: string) => {
    const keepId = keepSelections[groupKey];
    if (!keepId) return;
    const mergeIds = group.filter((b) => b.id !== keepId).map((b) => b.id);
    setMergeSuccess(null);
    await runOperationWithPolling(
      () => api.mergeBooks(keepId, mergeIds),
      setActiveOp,
      (final) => {
        if (final.status === 'failed') {
          setError(final.error_message || 'Merge failed');
        } else {
          setMergeSuccess(`Merged duplicates of "${group[0]?.title}"`);
          setGroups((prev) => prev.filter((_, i) => `group-${i}` !== groupKey));
          setSelectedGroups((prev) => {
            const next = new Set(prev);
            next.delete(groupKey);
            return next;
          });
        }
      },
      setError
    );
  };

  const handleMergeSelected = async () => {
    setMergeSuccess(null);
    for (let i = 0; i < groups.length; i++) {
      const groupKey = `group-${i}`;
      if (!selectedGroups.has(groupKey)) continue;
      const group = groups[i];
      const keepId = keepSelections[groupKey];
      if (!keepId) continue;
      const mergeIds = group.filter((b) => b.id !== keepId).map((b) => b.id);
      try {
        const initial = await api.mergeBooks(keepId, mergeIds);
        setActiveOp(initial);
        await api.pollOperation(initial.id, (update) => setActiveOp(update));
      } catch (err) {
        setError(err instanceof Error ? err.message : `Failed to merge "${group[0]?.title}"`);
      }
    }
    setActiveOp(null);
    setMergeSuccess(`Merged ${selectedGroups.size} selected group(s)`);
    fetchDuplicates();
  };

  const handleMergeAll = async () => {
    setConfirmOpen(false);
    setMergeSuccess(null);
    for (let i = 0; i < groups.length; i++) {
      const group = groups[i];
      const groupKey = `group-${i}`;
      const keepId = keepSelections[groupKey];
      if (!keepId) continue;
      const mergeIds = group.filter((b) => b.id !== keepId).map((b) => b.id);
      try {
        const initial = await api.mergeBooks(keepId, mergeIds);
        setActiveOp(initial);
        await api.pollOperation(initial.id, (update) => setActiveOp(update));
      } catch (err) {
        setError(err instanceof Error ? err.message : `Failed to merge "${group[0]?.title}"`);
      }
    }
    setActiveOp(null);
    setMergeSuccess('Merged all duplicate books');
    fetchDuplicates();
  };

  const toggleGroup = (key: string) => {
    setSelectedGroups((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  const toggleAll = () => {
    if (selectedGroups.size === groups.length) {
      setSelectedGroups(new Set());
    } else {
      setSelectedGroups(new Set(groups.map((_, i) => `group-${i}`)));
    }
  };

  const busy = activeOp !== null;

  return (
    <Box>
      <Box sx={{ display: 'flex', alignItems: 'center', mb: 2 }}>
        <Typography variant="body2" color="text.secondary" sx={{ flexGrow: 1 }}>
          Detects books with identical titles and authors at different file paths.
        </Typography>
        <Stack direction="row" spacing={1}>
          {groups.length > 0 && (
            <>
              <Button size="small" onClick={toggleAll} disabled={busy}>
                {selectedGroups.size === groups.length ? 'Deselect All' : 'Select All'}
              </Button>
              {selectedGroups.size > 0 && (
                <Button
                  variant="contained"
                  color="primary"
                  startIcon={<MergeIcon />}
                  onClick={handleMergeSelected}
                  disabled={busy}
                >
                  Merge Selected ({selectedGroups.size})
                </Button>
              )}
              <Button
                variant="contained"
                color="warning"
                startIcon={<MergeIcon />}
                onClick={() => setConfirmOpen(true)}
                disabled={busy}
              >
                Merge All ({totalDuplicates})
              </Button>
            </>
          )}
          <Tooltip title="Refresh">
            <IconButton onClick={fetchDuplicates} disabled={loading || busy}>
              <RefreshIcon />
            </IconButton>
          </Tooltip>
        </Stack>
      </Box>

      <OperationProgress operation={activeOp} />
      {error && (
        <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>
          {error}
        </Alert>
      )}
      {mergeSuccess && (
        <Alert
          severity="success"
          sx={{ mb: 2 }}
          icon={<CheckCircleIcon />}
          onClose={() => setMergeSuccess(null)}
        >
          {mergeSuccess}
        </Alert>
      )}

      {loading ? (
        <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}>
          <CircularProgress />
        </Box>
      ) : groups.length === 0 ? (
        <Paper sx={{ p: 4, textAlign: 'center' }}>
          <CheckCircleIcon sx={{ fontSize: 48, color: 'success.main', mb: 1 }} />
          <Typography variant="h6">No duplicate books found</Typography>
        </Paper>
      ) : (
        <>
          <PaginationControls
            total={groups.length}
            page={pagination.page}
            rowsPerPage={pagination.rowsPerPage}
            onPageChange={pagination.setPage}
            onRowsPerPageChange={pagination.setRowsPerPage}
          />
          <Stack spacing={2}>
            {groups.slice(pagination.startIdx, pagination.endIdx).map((group, sliceIdx) => {
              const idx = pagination.startIdx + sliceIdx;
              const groupKey = `group-${idx}`;
              return (
                <Card key={groupKey} variant="outlined">
                  <CardContent>
                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
                      <Checkbox
                        checked={selectedGroups.has(groupKey)}
                        onChange={() => toggleGroup(groupKey)}
                        disabled={busy}
                        size="small"
                      />
                      <Typography variant="subtitle1" fontWeight="bold">
                        {cleanDisplayTitle(group[0]?.title || 'Unknown')}
                      </Typography>
                      <Chip
                        label={`${group.length} copies`}
                        size="small"
                        color="warning"
                        variant="outlined"
                      />
                    </Box>
                    <Divider sx={{ my: 1 }} />
                    <RadioGroup
                      value={keepSelections[groupKey] || ''}
                      onChange={(e) =>
                        setKeepSelections((prev) => ({ ...prev, [groupKey]: e.target.value }))
                      }
                    >
                      {group.map((book) => (
                        <FormControlLabel
                          key={book.id}
                          value={book.id}
                          control={<Radio size="small" />}
                          label={
                            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                              <FolderIcon fontSize="small" color="action" />
                              <Typography
                                variant="body2"
                                sx={{ fontFamily: 'monospace', fontSize: '0.8rem' }}
                              >
                                {book.file_path}
                              </Typography>
                              {book.itunes_persistent_id && (
                                <Chip label="iTunes" size="small" color="info" variant="outlined" />
                              )}
                              {book.format && (
                                <Chip label={book.format} size="small" variant="outlined" />
                              )}
                            </Box>
                          }
                        />
                      ))}
                    </RadioGroup>
                  </CardContent>
                  <CardActions>
                    <Button
                      startIcon={<MergeIcon />}
                      variant="contained"
                      size="small"
                      onClick={() => handleMerge(group, groupKey)}
                      disabled={busy}
                    >
                      Merge
                    </Button>
                  </CardActions>
                </Card>
              );
            })}
          </Stack>
          <PaginationControls
            total={groups.length}
            page={pagination.page}
            rowsPerPage={pagination.rowsPerPage}
            onPageChange={pagination.setPage}
            onRowsPerPageChange={pagination.setRowsPerPage}
          />
        </>
      )}

      <Dialog open={confirmOpen} onClose={() => setConfirmOpen(false)}>
        <DialogTitle>Confirm Merge All</DialogTitle>
        <DialogContent>
          <DialogContentText>
            This will merge {groups.length} groups. This action cannot be undone. Are you sure?
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmOpen(false)}>Cancel</Button>
          <Button onClick={handleMergeAll} color="warning" variant="contained">
            Confirm
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
