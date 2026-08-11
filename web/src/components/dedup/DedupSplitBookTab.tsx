// file: web/src/components/dedup/DedupSplitBookTab.tsx
// version: 1.1.0
// guid: 7a8b9c0d-1e2f-3a4b-5c6d-7e8f9a0b1c2d
// last-edited: 2026-08-10
// Split-book backfill review tab (MAYDEPLOY-G3).
//
// Lists persisted split-book candidate clusters from
// GET /api/v1/dedup/split-book-candidates and lets the operator:
//
//   - Trigger a fresh scan (POST /api/v1/dedup/split-book-scan)
//   - Page through detected clusters
//   - Expand a row to see all book IDs (linked to the book detail page)
//   - Merge a single cluster (POST .../split-book-candidates/:id/merge)
//
// The backend currently has no dismiss endpoint for split-book candidates
// — surfaced as a tooltip on a disabled Dismiss button so the gap is
// visible to the operator. Tracked as a follow-up.
//
// Pagination is client-side because the backend returns the full list in
// one response — clusters are small (hundreds at most). This keeps the
// component self-contained and matches the no-pagination contract of
// listSplitBookCandidates.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Collapse,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  IconButton,
  Link,
  Paper,
  Snackbar,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TablePagination,
  TableRow,
  Tooltip,
  Typography,
} from '@mui/material';
import KeyboardArrowDownIcon from '@mui/icons-material/KeyboardArrowDown';
import KeyboardArrowUpIcon from '@mui/icons-material/KeyboardArrowUp';
import MergeIcon from '@mui/icons-material/MergeType';
import RefreshIcon from '@mui/icons-material/Refresh';
import PlayArrowIcon from '@mui/icons-material/PlayArrow';
import * as api from '../../services/api';
import type { SplitBookCandidate } from '../../services/api';
import { useOperationsStore } from '../../stores/useOperationsStore';

interface RowProps {
  candidate: SplitBookCandidate;
  expanded: boolean;
  onToggle: () => void;
  onMergeRequest: (c: SplitBookCandidate) => void;
  merging: boolean;
}

function CandidateRow({ candidate, expanded, onToggle, onMergeRequest, merging }: RowProps) {
  const bookCount = candidate.book_ids?.length ?? 0;
  return (
    <>
      <TableRow hover sx={{ '& > *': { borderBottom: 'unset' } }}>
        <TableCell padding="checkbox">
          <IconButton size="small" onClick={onToggle} aria-label="expand row">
            {expanded ? <KeyboardArrowUpIcon /> : <KeyboardArrowDownIcon />}
          </IconButton>
        </TableCell>
        <TableCell>
          <Typography
            variant="body2"
            sx={{ fontFamily: 'monospace', wordBreak: 'break-all' }}
          >
            {candidate.parent_folder || <em>(empty)</em>}
          </Typography>
          <Stack direction="row" spacing={1} sx={{ mt: 0.5 }}>
            <Chip
              size="small"
              label={candidate.shape || 'parent'}
              variant="outlined"
            />
            {candidate.sequential_pattern && (
              <Chip
                size="small"
                label={candidate.sequential_pattern}
                variant="outlined"
              />
            )}
          </Stack>
        </TableCell>
        <TableCell>
          <Typography variant="body2">
            {candidate.suggested_title || <em>(none)</em>}
          </Typography>
        </TableCell>
        <TableCell>
          <Typography variant="body2">
            {candidate.suggested_author || <em>(none)</em>}
          </Typography>
        </TableCell>
        <TableCell align="right">
          <Chip size="small" color="primary" label={bookCount} />
        </TableCell>
        <TableCell align="right">
          <Stack direction="row" spacing={1} justifyContent="flex-end">
            <Button
              size="small"
              variant="contained"
              startIcon={merging ? <CircularProgress size={14} /> : <MergeIcon />}
              disabled={merging || bookCount < 2}
              onClick={() => onMergeRequest(candidate)}
            >
              Merge
            </Button>
            <Tooltip title="Backend does not yet expose a dismiss endpoint for split-book candidates. Tracked as a follow-up.">
              <span>
                <Button size="small" variant="outlined" disabled>
                  Dismiss
                </Button>
              </span>
            </Tooltip>
          </Stack>
        </TableCell>
      </TableRow>
      <TableRow>
        <TableCell sx={{ pb: 0, pt: 0 }} colSpan={6}>
          <Collapse in={expanded} timeout="auto" unmountOnExit>
            <Box sx={{ m: 2 }}>
              <Typography variant="subtitle2" gutterBottom>
                Books in cluster ({bookCount})
              </Typography>
              <Stack direction="row" spacing={1} flexWrap="wrap" useFlexGap>
                {(candidate.book_ids ?? []).map((bid, idx) => (
                  <Link
                    key={bid}
                    component={RouterLink}
                    to={`/library/${encodeURIComponent(bid)}`}
                    underline="hover"
                    sx={{ fontFamily: 'monospace', fontSize: '0.85rem' }}
                  >
                    {bid}
                    {idx === 0 && (
                      <Chip
                        size="small"
                        label="suggested keep"
                        color="success"
                        sx={{ ml: 0.5 }}
                      />
                    )}
                  </Link>
                ))}
              </Stack>
            </Box>
          </Collapse>
        </TableCell>
      </TableRow>
    </>
  );
}
export function DedupSplitBookTab() {
  const [candidates, setCandidates] = useState<SplitBookCandidate[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [page, setPage] = useState(0);
  const [rowsPerPage, setRowsPerPage] = useState(25);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [scanning, setScanning] = useState(false);
  const [scanMsg, setScanMsg] = useState<string | null>(null);
  const [mergingId, setMergingId] = useState<string | null>(null);
  const [confirmCandidate, setConfirmCandidate] = useState<SplitBookCandidate | null>(null);
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const isUnmountedRef = useRef(false);
  const loadCandidates = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const resp = await api.getSplitBookCandidates();
      setCandidates(resp.candidates || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load split-book candidates');
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => {
    loadCandidates();
  }, [loadCandidates]);
  useEffect(() => {
    return () => {
      isUnmountedRef.current = true;
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
        timeoutRef.current = null;
      }
    };
  }, []);

  const handleScan = async () => {
    setScanning(true);
    setScanMsg(null);
    try {
      const op = await api.triggerSplitBookScan();
      if (op?.id && op?.type) {
        useOperationsStore.getState().startPolling(op.id, op.type);
        setScanMsg(`Split-book scan started (op ${op.id.slice(-6)})`);
      } else {
        setScanMsg('Split-book scan started');
      }
      if (timeoutRef.current) clearTimeout(timeoutRef.current);
      // Wait a moment for the scan to write candidates, then refresh.
      timeoutRef.current = setTimeout(() => {
        if (!isUnmountedRef.current) loadCandidates();
        timeoutRef.current = null;
      }, 3000);
    } catch (err) {
      setScanMsg(err instanceof Error ? err.message : 'Scan failed');
    } finally {
      setScanning(false);
    }
  };

  const handleToggle = (id: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const handleMergeRequest = (c: SplitBookCandidate) => {
    setConfirmCandidate(c);
  };

  const handleConfirmMerge = async () => {
    const c = confirmCandidate;
    setConfirmCandidate(null);
    if (!c) return;
    setMergingId(c.id);
    try {
      await api.mergeSplitBookCandidate(c.id);
      setScanMsg(`Merged cluster ${c.id.slice(-6)} (${c.book_ids.length} books)`);
      // Optimistically drop the merged candidate from local state, then refresh.
      setCandidates((prev) => prev.filter((x) => x.id !== c.id));
      loadCandidates();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Merge failed');
    } finally {
      setMergingId(null);
    }
  };

  const pageSlice = useMemo(() => {
    const start = page * rowsPerPage;
    return candidates.slice(start, start + rowsPerPage);
  }, [candidates, page, rowsPerPage]);

  const totalBooks = useMemo(
    () => candidates.reduce((sum, c) => sum + (c.book_ids?.length ?? 0), 0),
    [candidates]
  );

  return (
    <Box>
      <Paper sx={{ p: 2, mb: 2 }}>
        <Stack
          direction={{ xs: 'column', sm: 'row' }}
          spacing={2}
          alignItems={{ xs: 'flex-start', sm: 'center' }}
          justifyContent="space-between"
        >
          <Box>
            <Typography variant="h6">Split-book candidates</Typography>
            <Typography variant="body2" color="text.secondary">
              Clusters of one-Book-per-chapter rows that look like a single
              audiobook split across many records.
            </Typography>
            <Stack direction="row" spacing={2} sx={{ mt: 1 }}>
              <Chip
                size="small"
                label={`${candidates.length} clusters`}
                color="primary"
                variant="outlined"
              />
              <Chip
                size="small"
                label={`${totalBooks} books`}
                variant="outlined"
              />
            </Stack>
          </Box>
          <Stack direction="row" spacing={1}>
            <Button
              variant="outlined"
              startIcon={<RefreshIcon />}
              onClick={loadCandidates}
              disabled={loading}
            >
              Refresh
            </Button>
            <Button
              variant="contained"
              startIcon={scanning ? <CircularProgress size={16} /> : <PlayArrowIcon />}
              onClick={handleScan}
              disabled={scanning}
            >
              Trigger Scan
            </Button>
          </Stack>
        </Stack>
      </Paper>

      {error && (
        <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>
          {error}
        </Alert>
      )}

      <Paper>
        {loading ? (
          <Box sx={{ p: 4, textAlign: 'center' }}>
            <CircularProgress />
          </Box>
        ) : candidates.length === 0 ? (
          <Box sx={{ p: 4, textAlign: 'center' }}>
            <Typography variant="body2" color="text.secondary">
              No split-book candidates. Click <strong>Trigger Scan</strong> to detect
              chapter-cluster patterns in the library.
            </Typography>
          </Box>
        ) : (
          <>
            <TableContainer>
              <Table size="small">
                <TableHead>
                  <TableRow>
                    <TableCell />
                    <TableCell>Parent folder</TableCell>
                    <TableCell>Suggested title</TableCell>
                    <TableCell>Suggested author</TableCell>
                    <TableCell align="right">Books</TableCell>
                    <TableCell align="right">Actions</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {pageSlice.map((c) => (
                    <CandidateRow
                      key={c.id}
                      candidate={c}
                      expanded={expanded.has(c.id)}
                      onToggle={() => handleToggle(c.id)}
                      onMergeRequest={handleMergeRequest}
                      merging={mergingId === c.id}
                    />
                  ))}
                </TableBody>
              </Table>
            </TableContainer>
            <TablePagination
              component="div"
              count={candidates.length}
              page={page}
              onPageChange={(_, p) => setPage(p)}
              rowsPerPage={rowsPerPage}
              onRowsPerPageChange={(e) => {
                setRowsPerPage(parseInt(e.target.value, 10));
                setPage(0);
              }}
              rowsPerPageOptions={[10, 25, 50, 100]}
            />
          </>
        )}
      </Paper>

      <Dialog open={confirmCandidate !== null} onClose={() => setConfirmCandidate(null)}>
        <DialogTitle>Confirm split-book merge</DialogTitle>
        <DialogContent>
          <DialogContentText component="div">
            <Typography variant="body2" gutterBottom>
              Merge <strong>{confirmCandidate?.book_ids.length ?? 0}</strong> books into
              one record using the earliest ULID as the keeper. Source books will be
              soft-deleted and their files re-parented to the keep book.
            </Typography>
            <Box sx={{ mt: 2 }}>
              <Typography variant="caption" color="text.secondary">
                Parent folder
              </Typography>
              <Typography
                variant="body2"
                sx={{ fontFamily: 'monospace', wordBreak: 'break-all' }}
              >
                {confirmCandidate?.parent_folder}
              </Typography>
            </Box>
            <Box sx={{ mt: 1 }}>
              <Typography variant="caption" color="text.secondary">
                Suggested title
              </Typography>
              <Typography variant="body2">{confirmCandidate?.suggested_title}</Typography>
            </Box>
            <Box sx={{ mt: 1 }}>
              <Typography variant="caption" color="text.secondary">
                Keep book (earliest ULID)
              </Typography>
              <Typography variant="body2" sx={{ fontFamily: 'monospace' }}>
                {confirmCandidate?.book_ids?.[0]}
              </Typography>
            </Box>
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmCandidate(null)}>Cancel</Button>
          <Button
            variant="contained"
            color="primary"
            startIcon={<MergeIcon />}
            onClick={handleConfirmMerge}
          >
            Merge
          </Button>
        </DialogActions>
      </Dialog>

      <Snackbar
        open={!!scanMsg}
        autoHideDuration={6000}
        onClose={() => setScanMsg(null)}
        message={scanMsg ?? ''}
      />
    </Box>
  );
}
