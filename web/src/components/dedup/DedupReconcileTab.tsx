// file: web/src/components/dedup/DedupReconcileTab.tsx
// version: 1.0.2
// guid: d4e5f6a7-b8c9-0123-def0-123456789003
// last-edited: 2026-08-19

import { useState, useEffect } from 'react';
import {
  Box,
  Typography,
  Paper,
  Button,
  Alert,
  Chip,
  CircularProgress,
  Card,
  CardContent,
  Stack,
  Checkbox,
  LinearProgress,
} from '@mui/material';
import SearchIcon from '@mui/icons-material/Search';
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import * as api from '../../services/api';
import type { ReconcileMatch, ReconcilePreview, ReconcileBrokenRecord } from '../../services/api';

export function ReconcileTab() {
  const [scanning, setScanning] = useState(false);
  const [scanProgress, setScanProgress] = useState<{
    progress: number;
    total: number;
    message: string;
  } | null>(null);
  const [preview, setPreview] = useState<ReconcilePreview | null>(null);
  const [lastScanTime, setLastScanTime] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [applying, setApplying] = useState(false);
  const [applyResult, setApplyResult] = useState<string | null>(null);

  const autoSelectHighConfidence = (data: ReconcilePreview) => {
    const autoSelect = new Set<string>();
    for (const m of data.matches) {
      if (m.confidence === 'high') autoSelect.add(m.book_id);
    }
    setSelected(autoSelect);
  };

  useEffect(() => {
    const loadLatest = async () => {
      try {
        const { operation, preview: data } = await api.getLatestReconcileScan();
        if (operation && operation.status === 'running') {
          setScanning(true);
          pollForResult(operation.id);
        } else if (data) {
          setPreview(data);
          autoSelectHighConfidence(data);
          if (operation?.completed_at) setLastScanTime(operation.completed_at);
        }
      } catch {
        // No previous scan, that's fine
      }
    };
    loadLatest();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const pollForResult = async (opId: string) => {
    try {
      const result = await api.pollOperation(opId, (op) => {
        setScanProgress({ progress: op.progress, total: op.total, message: op.message });
      });
      if (result.status === 'completed') {
        const { preview: data } = await api.getLatestReconcileScan();
        if (data) {
          setPreview(data);
          autoSelectHighConfidence(data);
        }
        setLastScanTime(new Date().toISOString());
      } else {
        setError(`Scan ${result.status}: ${result.message || result.error_message || ''}`);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Scan failed');
    } finally {
      setScanning(false);
      setScanProgress(null);
    }
  };

  const startScan = async () => {
    setScanning(true);
    setError(null);
    setApplyResult(null);
    try {
      const op = await api.startReconcileScan();
      pollForResult(op.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to start scan');
      setScanning(false);
    }
  };

  const toggleMatch = (bookId: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(bookId)) next.delete(bookId);
      else next.add(bookId);
      return next;
    });
  };

  const selectAll = () => {
    if (!preview) return;
    setSelected(new Set(preview.matches.map((m) => m.book_id)));
  };

  const deselectAll = () => setSelected(new Set());

  const applyFixes = async () => {
    if (!preview || selected.size === 0) return;
    setApplying(true);
    setApplyResult(null);
    try {
      const matches = preview.matches
        .filter((m) => selected.has(m.book_id))
        .map((m) => ({ book_id: m.book_id, new_path: m.new_path }));
      const op = await api.startReconcile(matches);
      const result = await api.pollOperation(op.id);
      if (result.status === 'completed') {
        setApplyResult('Reconciliation completed successfully.');
        startScan();
      } else {
        setApplyResult(
          `Reconciliation ${result.status}: ${result.message || result.error_message || ''}`
        );
      }
    } catch (err) {
      setApplyResult(err instanceof Error ? err.message : 'Failed to apply fixes');
    } finally {
      setApplying(false);
    }
  };

  const confidenceColor = (confidence: string): 'success' | 'warning' | 'error' => {
    switch (confidence) {
      case 'high':
        return 'success';
      case 'medium':
        return 'warning';
      default:
        return 'error';
    }
  };

  const matchTypeLabel = (type: string): string => {
    switch (type) {
      case 'hash':
        return 'File Hash';
      case 'original_hash':
        return 'Original Hash';
      case 'filename':
        return 'Filename';
      default:
        return type;
    }
  };

  return (
    <Box>
      <Typography
        variant="body2"
        sx={{
          color: 'text.secondary',
          mb: 2,
        }}
      >
        Scan the library to find books whose file paths no longer exist on disk, then match them
        against untracked audio files by hash or filename. Scans run in the background — you can
        refresh the page and results will persist.
      </Typography>

      <Stack
        direction="row"
        spacing={2}
        sx={{
          alignItems: 'center',
          mb: 2,
        }}
      >
        <Button
          variant="contained"
          startIcon={scanning ? <CircularProgress size={16} /> : <SearchIcon />}
          onClick={startScan}
          disabled={scanning}
        >
          {scanning ? 'Scanning...' : 'Scan for Broken Links'}
        </Button>
        {lastScanTime && (
          <Typography
            variant="caption"
            sx={{
              color: 'text.secondary',
            }}
          >
            Last scan: {new Date(lastScanTime).toLocaleString()}
          </Typography>
        )}
      </Stack>

      {scanning && (
        <Paper sx={{ p: 2, mb: 2 }}>
          <Stack spacing={1}>
            <Typography variant="subtitle2">Scan in progress...</Typography>
            {scanProgress && (
              <>
                <LinearProgress
                  variant={scanProgress.total > 0 ? 'determinate' : 'indeterminate'}
                  value={
                    scanProgress.total > 0 ? (scanProgress.progress / scanProgress.total) * 100 : 0
                  }
                />
                <Typography
                  variant="body2"
                  sx={{
                    color: 'text.secondary',
                  }}
                >
                  {scanProgress.message}
                </Typography>
              </>
            )}
            {!scanProgress && <LinearProgress />}
            <Typography
              variant="caption"
              sx={{
                color: 'text.secondary',
              }}
            >
              You can navigate away and come back — results will be saved.
            </Typography>
          </Stack>
        </Paper>
      )}

      {error && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {error}
        </Alert>
      )}
      {applyResult && (
        <Alert severity="info" sx={{ mb: 2 }}>
          {applyResult}
        </Alert>
      )}

      {preview && (
        <>
          <Stack
            direction="row"
            spacing={2}
            useFlexGap
            sx={{
              flexWrap: 'wrap',
              mb: 2,
            }}
          >
            <Chip
              label={`${preview.broken_records.length} broken records`}
              color={preview.broken_records.length > 0 ? 'error' : 'success'}
            />
            <Chip label={`${preview.untracked_files.length} untracked files`} color="default" />
            <Chip label={`${preview.matches.length} matches found`} color="primary" />
            <Chip
              label={`${preview.unmatched_books.length} unmatched`}
              color={preview.unmatched_books.length > 0 ? 'warning' : 'success'}
            />
          </Stack>

          {preview.matches.length > 0 && (
            <Paper sx={{ p: 2, mb: 2 }}>
              <Stack
                direction="row"
                sx={{
                  justifyContent: 'space-between',
                  alignItems: 'center',
                  mb: 1,
                }}
              >
                <Typography variant="h6">Matches ({preview.matches.length})</Typography>
                <Stack direction="row" spacing={1}>
                  <Button size="small" onClick={selectAll}>
                    Select All
                  </Button>
                  <Button size="small" onClick={deselectAll}>
                    Deselect All
                  </Button>
                  <Button
                    variant="contained"
                    color="primary"
                    size="small"
                    onClick={applyFixes}
                    disabled={applying || selected.size === 0}
                    startIcon={applying ? <CircularProgress size={16} /> : <CheckCircleIcon />}
                  >
                    Apply {selected.size} Fix{selected.size !== 1 ? 'es' : ''}
                  </Button>
                </Stack>
              </Stack>

              {preview.matches.map((m: ReconcileMatch) => {
                const oldParts = m.old_path.split('/');
                const newParts = m.new_path.split('/');
                let commonIdx = 0;
                while (
                  commonIdx < oldParts.length - 1 &&
                  commonIdx < newParts.length - 1 &&
                  oldParts[commonIdx] === newParts[commonIdx]
                ) {
                  commonIdx++;
                }
                const commonPrefix = oldParts.slice(0, commonIdx).join('/');
                const oldSuffix = oldParts.slice(commonIdx).join('/');
                const newSuffix = newParts.slice(commonIdx).join('/');
                const hasCommon = commonIdx > 1;

                return (
                  <Card key={m.book_id} variant="outlined" sx={{ mb: 1 }}>
                    <CardContent sx={{ pb: 1 }}>
                      <Stack
                        direction="row"
                        spacing={1}
                        sx={{
                          alignItems: 'flex-start',
                        }}
                      >
                        <Checkbox
                          checked={selected.has(m.book_id)}
                          onChange={() => toggleMatch(m.book_id)}
                          sx={{ mt: -0.5 }}
                        />
                        <Box sx={{ flex: 1, minWidth: 0, overflow: 'hidden' }}>
                          <Stack
                            direction="row"
                            spacing={1}
                            sx={{
                              alignItems: 'center',
                              mb: 0.5,
                            }}
                          >
                            <Typography variant="subtitle2">{m.book_title}</Typography>
                            <Chip
                              label={matchTypeLabel(m.match_type)}
                              color={confidenceColor(m.confidence)}
                              size="small"
                            />
                            <Chip
                              label={m.confidence}
                              color={confidenceColor(m.confidence)}
                              size="small"
                              variant="outlined"
                            />
                          </Stack>
                          {hasCommon && (
                            <Typography
                              variant="body2"
                              sx={{
                                color: 'text.secondary',
                                fontFamily: 'monospace',
                                fontSize: '0.7rem',
                                opacity: 0.6,
                              }}
                            >
                              {commonPrefix}/
                            </Typography>
                          )}
                          <Typography
                            variant="body2"
                            sx={{
                              fontFamily: 'monospace',
                              fontSize: '0.75rem',
                              color: 'error.main',
                              overflowX: 'auto',
                              whiteSpace: 'nowrap',
                            }}
                          >
                            - {hasCommon ? oldSuffix : m.old_path}
                          </Typography>
                          <Typography
                            variant="body2"
                            sx={{
                              fontFamily: 'monospace',
                              fontSize: '0.75rem',
                              color: 'success.main',
                              overflowX: 'auto',
                              whiteSpace: 'nowrap',
                            }}
                          >
                            + {hasCommon ? newSuffix : m.new_path}
                          </Typography>
                        </Box>
                      </Stack>
                    </CardContent>
                  </Card>
                );
              })}
            </Paper>
          )}

          {preview.unmatched_books.length > 0 && (
            <Paper sx={{ p: 2, mb: 2 }}>
              <Typography variant="h6" sx={{ mb: 1 }}>
                Unmatched Books ({preview.unmatched_books.length})
              </Typography>
              <Typography
                variant="body2"
                sx={{
                  color: 'text.secondary',
                  mb: 1,
                }}
              >
                These books have missing files and no matching file was found on disk.
              </Typography>
              {preview.unmatched_books.map((b: ReconcileBrokenRecord) => (
                <Card key={b.book_id} variant="outlined" sx={{ mb: 1 }}>
                  <CardContent sx={{ pb: 1 }}>
                    <Typography variant="subtitle2">{b.title}</Typography>
                    <Typography
                      variant="body2"
                      noWrap
                      sx={{
                        color: 'text.secondary',
                        fontFamily: 'monospace',
                        fontSize: '0.75rem',
                      }}
                    >
                      {b.file_path}
                    </Typography>
                  </CardContent>
                </Card>
              ))}
            </Paper>
          )}

          {preview.broken_records.length === 0 && (
            <Alert severity="success">
              All book file paths are valid. No reconciliation needed.
            </Alert>
          )}
        </>
      )}
    </Box>
  );
}
