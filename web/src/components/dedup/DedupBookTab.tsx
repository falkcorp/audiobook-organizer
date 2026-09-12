// file: web/src/components/dedup/DedupBookTab.tsx
// version: 1.2.0
// guid: 71F51230-1BB6-4864-A1EB-120EE776D673
// last-edited: 2026-09-12

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

interface MergeFailure {
  title: string;
  reason: string;
}

// MergeReport is the outcome of any merge action -- bulk or single-group --
// that had at least one failure. It lives in its own state, not `error`,
// because fetchDuplicates() clears `error` as its first act (after a bulk
// merge, and on every Refresh), which used to erase the failure.
interface MergeReport {
  attempted: number;
  succeeded: number;
  failures: MergeFailure[];
}

// isMergeSuccess: only 'completed' is a merge that happened. pollOperation
// returns on every terminal status (failed, canceled, interrupted_*), so a
// check for 'failed' alone reports a canceled or interrupted merge as success.
function isMergeSuccess(final: Operation): boolean {
  return final.status === 'completed';
}

function describeMergeFailure(final: Operation): string {
  return final.error_message || `Merge ended with status "${final.status}"`;
}

export function DedupBookTab() {
  const [groups, setGroups] = useState<Book[][]>([]);
  const [totalDuplicates, setTotalDuplicates] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [activeOp, setActiveOp] = useState<Operation | null>(null);
  const [mergeSuccess, setMergeSuccess] = useState<string | null>(null);
  const [mergeReport, setMergeReport] = useState<MergeReport | null>(null);
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
    const title = cleanDisplayTitle(group[0]?.title || 'Unknown');
    // Both failure shapes -- a terminal status other than 'completed', and a
    // rejected request / throwing poll (runOperationWithPolling's onError) --
    // go into mergeReport so a later Refresh does not erase them.
    const reportFailure = (reason: string) =>
      setMergeReport({ attempted: 1, succeeded: 0, failures: [{ title, reason }] });
    setMergeSuccess(null);
    setMergeReport(null);
    await runOperationWithPolling(
      () => api.mergeBooks(keepId, mergeIds),
      setActiveOp,
      (final) => {
        if (!isMergeSuccess(final)) {
          reportFailure(describeMergeFailure(final));
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
      reportFailure
    );
  };

  // runBulkMerge merges each group in turn and records every group's outcome.
  // Each group is its own dedup.book-merge operation, so the server reports
  // per-group results: the POST can reject, or the op can reach a terminal
  // status other than 'completed'. pollOperation RESOLVES on failed / canceled /
  // interrupted_* rather than throwing, so both shapes are checked here.
  //
  // The outcome goes into mergeReport, not `error`: fetchDuplicates() clears
  // `error` as its first act, and it runs right after this loop. Routing
  // failures through setError is how a run with failed groups used to end with
  // only a success banner on screen.
  //
  // Groups are merged one at a time on purpose -- dedup.book-merge carries a
  // ConcurrencyKey, so the server would serialise them anyway.
  const runBulkMerge = async (indices: number[]) => {
    setMergeSuccess(null);
    setMergeReport(null);
    let attempted = 0;
    let succeeded = 0;
    const failures: MergeFailure[] = [];
    for (const i of indices) {
      const group = groups[i];
      const keepId = keepSelections[`group-${i}`];
      if (!group || !keepId) continue;
      const mergeIds = group.filter((b) => b.id !== keepId).map((b) => b.id);
      const title = cleanDisplayTitle(group[0]?.title || 'Unknown');
      attempted++;
      try {
        const initial = await api.mergeBooks(keepId, mergeIds);
        setActiveOp(initial);
        const final = await api.pollOperation(initial.id, (update) => setActiveOp(update));
        if (isMergeSuccess(final)) {
          succeeded++;
        } else {
          failures.push({ title, reason: describeMergeFailure(final) });
        }
      } catch (err) {
        failures.push({
          title,
          reason: err instanceof Error ? err.message : 'Merge request failed',
        });
      }
    }
    setActiveOp(null);
    if (attempted > 0) {
      if (failures.length === 0) {
        setMergeSuccess(`Merged ${succeeded} of ${attempted} group(s)`);
      } else {
        setMergeReport({ attempted, succeeded, failures });
      }
    }
    fetchDuplicates();
  };

  const handleMergeSelected = async () => {
    const indices = groups.map((_, i) => i).filter((i) => selectedGroups.has(`group-${i}`));
    await runBulkMerge(indices);
  };

  const handleMergeAll = async () => {
    setConfirmOpen(false);
    await runBulkMerge(groups.map((_, i) => i));
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
        <Typography
          variant="body2"
          sx={{
            color: 'text.secondary',
            flexGrow: 1,
          }}
        >
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
      {mergeReport && (
        <Alert
          severity={mergeReport.succeeded === 0 ? 'error' : 'warning'}
          sx={{ mb: 2 }}
          onClose={() => setMergeReport(null)}
          data-testid="bulk-merge-report"
        >
          <Typography variant="body2" sx={{ fontWeight: 'bold' }}>
            Merged {mergeReport.succeeded} of {mergeReport.attempted} group(s);{' '}
            {mergeReport.failures.length} failed:
          </Typography>
          <Box component="ul" sx={{ m: 0, pl: 2.5 }}>
            {mergeReport.failures.map((f, i) => (
              <li key={`${f.title}-${i}`}>
                <Typography variant="body2" component="span">
                  <strong>{f.title}</strong> — {f.reason}
                </Typography>
              </li>
            ))}
          </Box>
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
                      <Typography
                        variant="subtitle1"
                        sx={{
                          fontWeight: 'bold',
                        }}
                      >
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
