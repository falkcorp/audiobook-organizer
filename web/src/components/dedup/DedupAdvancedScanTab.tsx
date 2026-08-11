// file: web/src/components/dedup/DedupAdvancedScanTab.tsx
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-08-10
import { useState, useEffect, useCallback, useMemo } from 'react';
import {
  Box,
  Typography,
  Paper,
  Button,
  Alert,
  Chip,
  CircularProgress,
  Divider,
  IconButton,
  Tooltip,
  Card,
  CardContent,
  CardActions,
  Stack,
  Tab,
  Tabs,
} from '@mui/material';
import MergeIcon from '@mui/icons-material/MergeType';
import RefreshIcon from '@mui/icons-material/Refresh';
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import FolderIcon from '@mui/icons-material/Folder';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import VisibilityOffIcon from '@mui/icons-material/VisibilityOff';
import SearchIcon from '@mui/icons-material/Search';
import * as api from '../../services/api';
import type { BookDedupGroup, Operation } from '../../services/api';
import { useAsyncAction } from '../../hooks/useAsyncAction';
import {
  cleanDisplayTitle,
  OperationProgress,
  runOperationWithPolling,
  usePagination,
  PaginationControls,
} from './dedupHelpers';

export function BookDedupScanTab() {
  const [groups, setGroups] = useState<BookDedupGroup[]>([]);
  const [totalDuplicates, setTotalDuplicates] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [activeOp, setActiveOp] = useState<Operation | null>(null);
  const [mergeSuccess, setMergeSuccess] = useState<string | null>(null);
  const [needsRefresh, setNeedsRefresh] = useState(false);
  const [confidenceFilter, setConfidenceFilter] = useState<'all' | 'high' | 'medium' | 'low'>('all');
  const pagination = usePagination(groups.length);

  const { loading, run: performFetch } = useAsyncAction(async () => {
    setError(null);
    const data = await api.getBookDedupScanResults();
    setGroups(data.groups || []);
    setTotalDuplicates(data.duplicate_count || 0);
    setNeedsRefresh(data.needs_refresh || false);
  });

  const fetchResults = useCallback(() => performFetch(), [performFetch]);

  useEffect(() => { fetchResults(); }, [fetchResults]);

  const handleScan = async () => {
    setMergeSuccess(null);
    await runOperationWithPolling(
      () => api.scanBookDuplicates(),
      setActiveOp,
      (final) => {
        if (final.status === 'failed') {
          setError(final.error_message || 'Scan failed');
        } else {
          setMergeSuccess('Scan complete');
          fetchResults();
        }
      },
      (msg) => setError(msg),
    );
  };

  const handleMerge = async (group: BookDedupGroup) => {
    setMergeSuccess(null);
    setError(null);
    try {
      const bookIds = group.books.map(b => b.id);
      const result = await api.mergeBookDuplicatesAsVersions(bookIds);
      setMergeSuccess(result.message);
      setGroups(prev => prev.filter(g => g.group_key !== group.group_key));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Merge failed');
    }
  };

  const handleDismiss = async (group: BookDedupGroup) => {
    setError(null);
    try {
      await api.dismissBookDuplicateGroup(group.group_key);
      setGroups(prev => prev.filter(g => g.group_key !== group.group_key));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Dismiss failed');
    }
  };

  const filteredGroups = confidenceFilter === 'all'
    ? groups
    : groups.filter(g => g.confidence === confidenceFilter);

  const confidenceCounts = useMemo(() => {
    const counts = { high: 0, medium: 0, low: 0 };
    for (const g of groups) {
      if (g.confidence in counts) counts[g.confidence as keyof typeof counts]++;
    }
    return counts;
  }, [groups]);

  const confidenceColor = (c: string) => {
    switch (c) {
      case 'high': return 'error';
      case 'medium': return 'warning';
      case 'low': return 'info';
      default: return 'default';
    }
  };

  const formatFileSize = (bytes?: number) => {
    if (!bytes) return '--';
    if (bytes > 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
    if (bytes > 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
    return `${(bytes / 1024).toFixed(0)} KB`;
  };

  const formatDuration = (secs?: number) => {
    if (!secs) return '--';
    const h = Math.floor(secs / 3600);
    const m = Math.floor((secs % 3600) / 60);
    return h > 0 ? `${h}h ${m}m` : `${m}m`;
  };

  const busy = activeOp !== null;

  return (
    <Box>
      <Box sx={{ display: 'flex', alignItems: 'center', mb: 2 }}>
        <Typography variant="body2" color="text.secondary" sx={{ flexGrow: 1 }}>
          Advanced duplicate detection using file hashes, folder structure, and fuzzy title/author matching.
        </Typography>
        <Stack direction="row" spacing={1}>
          <Button variant="contained" startIcon={<SearchIcon />} onClick={handleScan} disabled={busy}>
            {needsRefresh ? 'Run Scan' : 'Re-Scan'}
          </Button>
          <Tooltip title="Refresh cached results">
            <IconButton onClick={fetchResults} disabled={loading || busy}><RefreshIcon /></IconButton>
          </Tooltip>
        </Stack>
      </Box>

      <OperationProgress operation={activeOp} label="Book Duplicate Scan" />
      {error && <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>{error}</Alert>}
      {mergeSuccess && <Alert severity="success" sx={{ mb: 2 }} icon={<CheckCircleIcon />} onClose={() => setMergeSuccess(null)}>{mergeSuccess}</Alert>}

      {loading ? (
        <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}><CircularProgress /></Box>
      ) : needsRefresh && groups.length === 0 ? (
        <Paper sx={{ p: 4, textAlign: 'center' }}>
          <ContentCopyIcon sx={{ fontSize: 48, color: 'text.secondary', mb: 1 }} />
          <Typography variant="h6">No scan results yet</Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
            Click &quot;Run Scan&quot; to detect duplicate books using hashes, folder structure, and metadata matching.
          </Typography>
        </Paper>
      ) : groups.length === 0 ? (
        <Paper sx={{ p: 4, textAlign: 'center' }}>
          <CheckCircleIcon sx={{ fontSize: 48, color: 'success.main', mb: 1 }} />
          <Typography variant="h6">No duplicate books found</Typography>
        </Paper>
      ) : (
        <>
          {/* Confidence filter tabs */}
          <Tabs value={confidenceFilter} onChange={(_, v) => setConfidenceFilter(v)} sx={{ mb: 2 }}>
            <Tab value="all" label={`All (${groups.length})`} />
            <Tab value="high" label={`High (${confidenceCounts.high})`} />
            <Tab value="medium" label={`Medium (${confidenceCounts.medium})`} />
            <Tab value="low" label={`Low (${confidenceCounts.low})`} />
          </Tabs>

          <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
            {totalDuplicates} total duplicates across {groups.length} groups
          </Typography>

          <PaginationControls total={filteredGroups.length} page={pagination.page} rowsPerPage={pagination.rowsPerPage}
            onPageChange={pagination.setPage} onRowsPerPageChange={pagination.setRowsPerPage} />

          <Stack spacing={2}>
            {filteredGroups.slice(pagination.startIdx, pagination.endIdx).map((group) => (
              <Card key={group.group_key} variant="outlined">
                <CardContent>
                  <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
                    <Typography variant="subtitle1" fontWeight="bold">
                      {cleanDisplayTitle(group.books[0]?.title || 'Unknown')}
                    </Typography>
                    {group.books[0]?.author_name && (
                      <Typography variant="body2" color="text.secondary">
                        by {group.books[0].author_name}
                      </Typography>
                    )}
                    <Chip label={`${group.books.length} copies`} size="small" color="warning" variant="outlined" />
                    <Chip label={group.confidence} size="small" color={confidenceColor(group.confidence) as 'error' | 'warning' | 'info' | 'default'} />
                    <Typography variant="caption" color="text.secondary">{group.reason}</Typography>
                  </Box>
                  <Divider sx={{ my: 1 }} />
                  {/* Table of duplicate books */}
                  <Box component="table" sx={{ width: '100%', borderCollapse: 'collapse', '& td, & th': { py: 0.5, px: 1, fontSize: '0.85rem' } }}>
                    <thead>
                      <tr>
                        <th style={{ textAlign: 'left' }}>File Path</th>
                        <th>Format</th>
                        <th>Bitrate</th>
                        <th>Duration</th>
                        <th>Size</th>
                      </tr>
                    </thead>
                    <tbody>
                      {group.books.map((book) => (
                        <tr key={book.id}>
                          <td>
                            <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
                              <FolderIcon fontSize="small" color="action" />
                              <Typography variant="body2" sx={{ fontFamily: 'monospace', fontSize: '0.75rem' }} noWrap title={book.file_path}>
                                {book.file_path}
                              </Typography>
                              {book.itunes_persistent_id && <Chip label="iTunes" size="small" color="info" variant="outlined" sx={{ ml: 0.5 }} />}
                            </Box>
                          </td>
                          <td style={{ textAlign: 'center' }}>
                            {book.format ? <Chip label={book.format.toUpperCase()} size="small" variant="outlined" /> : '--'}
                          </td>
                          <td style={{ textAlign: 'center' }}>
                            {book.bitrate ? `${book.bitrate} kbps` : '--'}
                          </td>
                          <td style={{ textAlign: 'center' }}>{formatDuration(book.duration)}</td>
                          <td style={{ textAlign: 'center' }}>{formatFileSize(book.file_size)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </Box>
                </CardContent>
                <CardActions>
                  <Button startIcon={<MergeIcon />} variant="contained" size="small"
                    onClick={() => handleMerge(group)} disabled={busy}>
                    Merge as Versions
                  </Button>
                  <Button startIcon={<VisibilityOffIcon />} size="small" color="inherit"
                    onClick={() => handleDismiss(group)} disabled={busy}>
                    Dismiss
                  </Button>
                </CardActions>
              </Card>
            ))}
          </Stack>
          <PaginationControls total={filteredGroups.length} page={pagination.page} rowsPerPage={pagination.rowsPerPage}
            onPageChange={pagination.setPage} onRowsPerPageChange={pagination.setRowsPerPage} />
        </>
      )}
    </Box>
  );
}