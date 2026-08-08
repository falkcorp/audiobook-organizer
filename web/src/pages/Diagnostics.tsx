// file: web/src/pages/Diagnostics.tsx
// version: 1.3.1
// last-edited: 2026-08-07
// guid: f2323fc4-b3e7-4298-9ec5-759447cbd643

import { useState, useCallback, useRef, useEffect } from 'react';
import { AIJobsPanel } from '../components/AIJobsPanel';
import { CacheStatsPanel } from '../components/CacheStatsPanel';
import {
  Alert,
  Accordion,
  AccordionSummary,
  AccordionDetails,
  Box,
  Button,
  Card,
  CardActionArea,
  CardContent,
  Checkbox,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  FormControlLabel,
  Grid,
  LinearProgress,
  Paper,
  Stack,
  Switch,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from '@mui/material';
import BugReportIcon from '@mui/icons-material/BugReport';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import SpellcheckIcon from '@mui/icons-material/Spellcheck';
import BuildIcon from '@mui/icons-material/Build';
import DownloadIcon from '@mui/icons-material/Download';
import SmartToyIcon from '@mui/icons-material/SmartToy';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import * as api from '../services/api';

interface CategoryOption {
  id: string;
  label: string;
  description: string;
  icon: React.ReactNode;
}

const categories: CategoryOption[] = [
  {
    id: 'errors',
    label: 'Error Analysis',
    description: 'Analyze recent errors and operation failures',
    icon: <BugReportIcon sx={{ fontSize: 40 }} />,
  },
  {
    id: 'dedup',
    label: 'Deduplication',
    description: 'Find duplicate books, orphan tracks, and missing merges',
    icon: <ContentCopyIcon sx={{ fontSize: 40 }} />,
  },
  {
    id: 'metadata',
    label: 'Metadata Quality',
    description: 'Check for incorrect authors, bad titles, missing series',
    icon: <SpellcheckIcon sx={{ fontSize: 40 }} />,
  },
  {
    id: 'general',
    label: 'General',
    description: 'Full library diagnostic with all checks',
    icon: <BuildIcon sx={{ fontSize: 40 }} />,
  },
];

const actionLabels: Record<string, string> = {
  merge_versions: 'Merge Versions',
  delete_orphan: 'Delete Orphans',
  fix_metadata: 'Fix Metadata',
  reassign_series: 'Reassign Series',
};

export function Diagnostics() {
  const [selectedCategory, setSelectedCategory] = useState<string | null>(null);
  const [description, setDescription] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [successMsg, setSuccessMsg] = useState<string | null>(null);

  // Operation state
  const [operationId, setOperationId] = useState<string | null>(null);
  const [operationType, setOperationType] = useState<'export' | 'ai' | null>(null);
  const [operationStatus, setOperationStatus] = useState<string | null>(null);
  const [operationProgress, setOperationProgress] = useState(0);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  // AI results
  const [aiResults, setAiResults] = useState<api.DiagnosticsAIResults | null>(null);
  const [selectedSuggestions, setSelectedSuggestions] = useState<Set<string>>(new Set());
  const [showRaw, setShowRaw] = useState(false);
  const [applyDialogOpen, setApplyDialogOpen] = useState(false);
  const [applying, setApplying] = useState(false);

  // DB health stats
  const [dbHealth, setDbHealth] = useState<api.DBHealthStats | null>(null);
  const [dbHealthLoading, setDbHealthLoading] = useState(false);
  const [dbHealthError, setDbHealthError] = useState<string | null>(null);

  const fetchDBHealth = useCallback(async () => {
    setDbHealthLoading(true);
    setDbHealthError(null);
    try {
      const stats = await api.getDBHealthStats();
      setDbHealth(stats);
    } catch (e) {
      setDbHealthError(e instanceof Error ? e.message : 'Failed to load DB health');
    } finally {
      setDbHealthLoading(false);
    }
  }, []);

  // Cleanup polling on unmount
  useEffect(() => {
    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, []);

  const stopPolling = useCallback(() => {
    if (pollRef.current) {
      clearInterval(pollRef.current);
      pollRef.current = null;
    }
  }, []);

  const startPolling = useCallback(
    (opId: string, type: 'export' | 'ai') => {
      stopPolling();
      setOperationId(opId);
      setOperationType(type);
      setOperationStatus('running');
      setOperationProgress(0);

      pollRef.current = setInterval(async () => {
        try {
          const op = await api.getOperationStatus(opId);
          setOperationStatus(op.status);
          if (op.progress !== undefined) setOperationProgress(op.progress);

          if (op.status === 'completed') {
            stopPolling();
            if (type === 'export') {
              // Trigger download
              try {
                const blob = await api.downloadDiagnosticsExport(opId);
                const url = URL.createObjectURL(blob);
                const a = document.createElement('a');
                a.href = url;
                a.download = `diagnostics-${opId}.zip`;
                document.body.appendChild(a);
                a.click();
                document.body.removeChild(a);
                URL.revokeObjectURL(url);
                setSuccessMsg('Diagnostics export downloaded');
              } catch (err) {
                setError(err instanceof Error ? err.message : 'Failed to download export');
              }
            } else if (type === 'ai') {
              // Fetch AI results
              try {
                const results = await api.getDiagnosticsAIResults(opId);
                setAiResults(results);
                setSelectedSuggestions(new Set());
                setSuccessMsg(
                  `AI analysis complete: ${results.suggestions.length} suggestion(s) found`
                );
              } catch (err) {
                setError(err instanceof Error ? err.message : 'Failed to get AI results');
              }
            }
            setOperationStatus('completed');
          } else if (op.status === 'failed') {
            stopPolling();
            setError(`Operation failed: ${op.errors?.[0] || 'Unknown error'}`);
            setOperationStatus('failed');
          } else if (op.status === 'cancelled') {
            stopPolling();
            setOperationStatus('cancelled');
          }
        } catch (err) {
          stopPolling();
          setError(err instanceof Error ? err.message : 'Failed to poll operation');
        }
      }, 5000);
    },
    [stopPolling]
  );

  const handleExport = async () => {
    if (!selectedCategory) return;
    setError(null);
    setSuccessMsg(null);
    try {
      const result = await api.startDiagnosticsExport(selectedCategory, description);
      startPolling(result.operation_id, 'export');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to start export');
    }
  };

  const handleSubmitAI = async () => {
    if (!selectedCategory) return;
    setError(null);
    setSuccessMsg(null);
    setAiResults(null);
    try {
      const result = await api.submitDiagnosticsAI(selectedCategory, description);
      setSuccessMsg(`AI analysis submitted: ${result.request_count} request(s)`);
      startPolling(result.operation_id, 'ai');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to submit AI analysis');
    }
  };

  const toggleSuggestion = (id: string) => {
    setSelectedSuggestions((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  };

  const selectAllInGroup = (suggestions: api.DiagnosticsSuggestion[]) => {
    setSelectedSuggestions((prev) => {
      const next = new Set(prev);
      for (const s of suggestions) {
        if (!s.applied) next.add(s.id);
      }
      return next;
    });
  };

  const deselectAllInGroup = (suggestions: api.DiagnosticsSuggestion[]) => {
    setSelectedSuggestions((prev) => {
      const next = new Set(prev);
      for (const s of suggestions) {
        next.delete(s.id);
      }
      return next;
    });
  };

  const handleApply = async () => {
    if (!operationId || selectedSuggestions.size === 0) return;
    setApplyDialogOpen(false);
    setApplying(true);
    setError(null);
    try {
      const result = await api.applyDiagnosticsSuggestions(
        operationId,
        Array.from(selectedSuggestions)
      );
      setSuccessMsg(
        `Applied ${result.applied} suggestion(s)` +
          (result.failed > 0 ? `, ${result.failed} failed` : '')
      );
      if (result.errors.length > 0) {
        setError(result.errors.join('; '));
      }
      // Refresh results
      if (operationId) {
        const results = await api.getDiagnosticsAIResults(operationId);
        setAiResults(results);
        setSelectedSuggestions(new Set());
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to apply suggestions');
    } finally {
      setApplying(false);
    }
  };

  // Group suggestions by action type
  const groupedSuggestions = aiResults
    ? Object.entries(
        aiResults.suggestions.reduce(
          (acc, s) => {
            if (!acc[s.action]) acc[s.action] = [];
            acc[s.action].push(s);
            return acc;
          },
          {} as Record<string, api.DiagnosticsSuggestion[]>
        )
      )
    : [];

  const isRunning = operationStatus === 'running' || operationStatus === 'pending';

  return (
    <Box sx={{ p: 3 }}>
      <Typography variant="h4" gutterBottom>
        Diagnostics
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
        Export library data for analysis or submit to AI for automated suggestions.
      </Typography>

      {error && (
        <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>
          {error}
        </Alert>
      )}
      {successMsg && (
        <Alert severity="success" sx={{ mb: 2 }} onClose={() => setSuccessMsg(null)}>
          {successMsg}
        </Alert>
      )}

      {/* Category Selection */}
      <Typography variant="h6" sx={{ mb: 1 }}>
        Select Category
      </Typography>
      <Grid container spacing={2} sx={{ mb: 3 }}>
        {categories.map((cat) => (
          <Grid item xs={12} sm={6} key={cat.id}>
            <Card
              variant="outlined"
              sx={{
                border: selectedCategory === cat.id ? 2 : 1,
                borderColor:
                  selectedCategory === cat.id ? 'primary.main' : 'divider',
              }}
            >
              <CardActionArea onClick={() => setSelectedCategory(cat.id)}>
                <CardContent>
                  <Stack direction="row" spacing={2} alignItems="center">
                    <Box sx={{ color: selectedCategory === cat.id ? 'primary.main' : 'text.secondary' }}>
                      {cat.icon}
                    </Box>
                    <Box>
                      <Typography variant="subtitle1" fontWeight="bold">
                        {cat.label}
                      </Typography>
                      <Typography variant="body2" color="text.secondary">
                        {cat.description}
                      </Typography>
                    </Box>
                  </Stack>
                </CardContent>
              </CardActionArea>
            </Card>
          </Grid>
        ))}
      </Grid>

      {/* Description */}
      <TextField
        label="Description"
        placeholder="Describe what you're investigating (optional)"
        multiline
        minRows={2}
        maxRows={4}
        fullWidth
        value={description}
        onChange={(e) => setDescription(e.target.value)}
        sx={{ mb: 3 }}
      />

      {/* Actions */}
      <Stack direction="row" spacing={2} sx={{ mb: 3 }}>
        <Button
          variant="outlined"
          startIcon={<DownloadIcon />}
          disabled={!selectedCategory || isRunning}
          onClick={handleExport}
        >
          Download ZIP
        </Button>
        <Button
          variant="contained"
          startIcon={<SmartToyIcon />}
          disabled={!selectedCategory || isRunning}
          onClick={handleSubmitAI}
        >
          Submit to AI
        </Button>
      </Stack>

      {/* Progress */}
      {isRunning && (
        <Box sx={{ mb: 3 }}>
          <Stack direction="row" spacing={1} alignItems="center" sx={{ mb: 1 }}>
            <Typography variant="body2" color="text.secondary">
              {operationType === 'export' ? 'Generating export...' : 'AI analysis in progress...'}
            </Typography>
            <Chip size="small" label={operationStatus} color="info" variant="outlined" />
          </Stack>
          <LinearProgress
            variant={operationProgress > 0 ? 'determinate' : 'indeterminate'}
            value={operationProgress}
          />
        </Box>
      )}

      {/* AI Results Panel */}
      {aiResults && (
        <Box sx={{ mt: 2 }}>
          <Stack
            direction="row"
            justifyContent="space-between"
            alignItems="center"
            sx={{ mb: 2 }}
          >
            <Typography variant="h6">
              AI Suggestions ({aiResults.suggestions.length})
            </Typography>
            <Stack direction="row" spacing={2} alignItems="center">
              <FormControlLabel
                control={
                  <Switch
                    checked={showRaw}
                    onChange={(e) => setShowRaw(e.target.checked)}
                    size="small"
                  />
                }
                label="View Raw"
              />
              <Button
                variant="contained"
                color="warning"
                disabled={selectedSuggestions.size === 0 || applying}
                onClick={() => setApplyDialogOpen(true)}
              >
                {applying
                  ? 'Applying...'
                  : `Apply Selected (${selectedSuggestions.size})`}
              </Button>
            </Stack>
          </Stack>

          {showRaw && (
            <Box
              component="pre"
              sx={{
                p: 2,
                mb: 2,
                bgcolor: 'grey.100',
                borderRadius: 1,
                overflow: 'auto',
                maxHeight: 400,
                fontSize: '0.75rem',
              }}
            >
              {JSON.stringify(aiResults.raw_responses, null, 2)}
            </Box>
          )}

          {groupedSuggestions.length === 0 && (
            <Typography color="text.secondary">
              No suggestions found.
            </Typography>
          )}

          {groupedSuggestions.map(([action, suggestions]) => (
            <Accordion key={action} defaultExpanded>
              <AccordionSummary expandIcon={<ExpandMoreIcon />}>
                <Stack direction="row" spacing={1} alignItems="center">
                  <Typography variant="subtitle1" fontWeight="bold">
                    {actionLabels[action] || action}
                  </Typography>
                  <Chip size="small" label={suggestions.length} />
                </Stack>
              </AccordionSummary>
              <AccordionDetails>
                <Stack direction="row" spacing={1} sx={{ mb: 1 }}>
                  <Button
                    size="small"
                    onClick={() => selectAllInGroup(suggestions)}
                  >
                    Select All
                  </Button>
                  <Button
                    size="small"
                    onClick={() => deselectAllInGroup(suggestions)}
                  >
                    Deselect All
                  </Button>
                </Stack>
                <Stack spacing={1}>
                  {suggestions.map((s) => (
                    <Card key={s.id} variant="outlined">
                      <CardContent
                        sx={{
                          display: 'flex',
                          alignItems: 'flex-start',
                          gap: 1,
                          py: 1.5,
                          '&:last-child': { pb: 1.5 },
                        }}
                      >
                        <Checkbox
                          checked={selectedSuggestions.has(s.id) || s.applied}
                          disabled={s.applied}
                          onChange={() => toggleSuggestion(s.id)}
                          sx={{ mt: -0.5 }}
                        />
                        <Box sx={{ flex: 1, minWidth: 0 }}>
                          <Typography variant="body2">
                            {s.reason}
                          </Typography>
                          <Typography
                            variant="caption"
                            color="text.secondary"
                          >
                            Books: {s.book_ids.join(', ')}
                            {s.primary_id && ` | Primary: ${s.primary_id}`}
                          </Typography>
                          {s.fix && (
                            <Typography
                              variant="caption"
                              color="text.secondary"
                              component="div"
                            >
                              Fix: {JSON.stringify(s.fix)}
                            </Typography>
                          )}
                        </Box>
                        {s.applied && (
                          <Chip
                            size="small"
                            label="Applied"
                            color="success"
                            variant="outlined"
                          />
                        )}
                      </CardContent>
                    </Card>
                  ))}
                </Stack>
              </AccordionDetails>
            </Accordion>
          ))}
        </Box>
      )}

      {/* Apply Confirmation Dialog */}
      <Dialog
        open={applyDialogOpen}
        onClose={() => setApplyDialogOpen(false)}
      >
        <DialogTitle>Apply Suggestions</DialogTitle>
        <DialogContent>
          <DialogContentText>
            Are you sure you want to apply {selectedSuggestions.size} selected
            suggestion(s)? This will modify your library data.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setApplyDialogOpen(false)}>Cancel</Button>
          <Button onClick={handleApply} color="warning" variant="contained">
            Apply
          </Button>
        </DialogActions>
      </Dialog>

      {/* AI Jobs Panel */}
      <AIJobsPanel />

      {/* DB Health Section */}
      <Accordion>
        <AccordionSummary expandIcon={<ExpandMoreIcon />} onClick={() => { if (!dbHealth && !dbHealthLoading) fetchDBHealth(); }}>
          <Typography variant="h6" fontWeight="bold">Database Health</Typography>
        </AccordionSummary>
        <AccordionDetails>
          <Stack spacing={2}>
            <Box sx={{ display: 'flex', justifyContent: 'flex-end' }}>
              <Button size="small" variant="outlined" onClick={fetchDBHealth} disabled={dbHealthLoading}>
                {dbHealthLoading ? <CircularProgress size={16} sx={{ mr: 1 }} /> : null}
                Refresh
              </Button>
            </Box>

            {dbHealthError && <Alert severity="error">{dbHealthError}</Alert>}

            {dbHealth && (
              <Stack spacing={2}>
                {/* SQLite main store tables */}
                {dbHealth.sqlite && (
                  <Paper variant="outlined" sx={{ p: 2 }}>
                    <Typography variant="subtitle2" fontWeight="bold" gutterBottom>
                      SQLite — Main Store ({(dbHealth.sqlite.size_bytes / 1024 / 1024).toFixed(1)} MB)
                    </Typography>
                    <Table size="small">
                      <TableHead>
                        <TableRow>
                          <TableCell>Table</TableCell>
                          <TableCell align="right">Row Count</TableCell>
                        </TableRow>
                      </TableHead>
                      <TableBody>
                        {dbHealth.sqlite.tables.map((t) => (
                          <TableRow key={t.name}>
                            <TableCell>{t.name}</TableCell>
                            <TableCell align="right">{t.row_count.toLocaleString()}</TableCell>
                          </TableRow>
                        ))}
                      </TableBody>
                    </Table>
                  </Paper>
                )}

                {/* PebbleDB main store */}
                {dbHealth.pebble && (
                  <Paper variant="outlined" sx={{ p: 2 }}>
                    <Typography variant="subtitle2" fontWeight="bold" gutterBottom>
                      PebbleDB — Main Store
                    </Typography>
                    <Stack direction="row" spacing={4}>
                      <Box>
                        <Typography variant="caption" color="text.secondary">Keys</Typography>
                        <Typography>{dbHealth.pebble.key_count.toLocaleString()}</Typography>
                      </Box>
                      <Box>
                        <Typography variant="caption" color="text.secondary">Size</Typography>
                        <Typography>{(dbHealth.pebble.size_bytes / 1024 / 1024).toFixed(1)} MB</Typography>
                      </Box>
                    </Stack>
                  </Paper>
                )}

                {/* Embeddings + AI Scans + Metadata cache as grid */}
                <Grid container spacing={2}>
                  <Grid item xs={12} sm={4}>
                    <Paper variant="outlined" sx={{ p: 2 }}>
                      <Typography variant="subtitle2" fontWeight="bold" gutterBottom>Embeddings DB</Typography>
                      <Stack spacing={0.5}>
                        <Box><Typography variant="caption" color="text.secondary">Vectors</Typography><Typography>{dbHealth.embeddings.vector_count.toLocaleString()}</Typography></Box>
                        <Box><Typography variant="caption" color="text.secondary">Size</Typography><Typography>{(dbHealth.embeddings.size_bytes / 1024 / 1024).toFixed(1)} MB</Typography></Box>
                      </Stack>
                    </Paper>
                  </Grid>
                  <Grid item xs={12} sm={4}>
                    <Paper variant="outlined" sx={{ p: 2 }}>
                      <Typography variant="subtitle2" fontWeight="bold" gutterBottom>AI Scans DB</Typography>
                      <Stack spacing={0.5}>
                        <Box><Typography variant="caption" color="text.secondary">Jobs</Typography><Typography>{dbHealth.ai_scans.job_count.toLocaleString()}</Typography></Box>
                        <Box><Typography variant="caption" color="text.secondary">Pending</Typography><Typography>{dbHealth.ai_scans.pending_count.toLocaleString()}</Typography></Box>
                        <Box><Typography variant="caption" color="text.secondary">Size</Typography><Typography>{(dbHealth.ai_scans.size_bytes / 1024 / 1024).toFixed(1)} MB</Typography></Box>
                      </Stack>
                    </Paper>
                  </Grid>
                  <Grid item xs={12} sm={4}>
                    <Paper variant="outlined" sx={{ p: 2 }}>
                      <Typography variant="subtitle2" fontWeight="bold" gutterBottom>Metadata Cache</Typography>
                      <Stack spacing={0.5}>
                        <Box><Typography variant="caption" color="text.secondary">Total entries</Typography><Typography>{dbHealth.metadata_cache.total_entries.toLocaleString()}</Typography></Box>
                        <Box><Typography variant="caption" color="text.secondary">TTL</Typography><Typography>{dbHealth.metadata_cache.ttl_days} days</Typography></Box>
                        <Box><Typography variant="caption" color="text.secondary">Expired</Typography><Typography>{dbHealth.metadata_cache.expired_entries.toLocaleString()}</Typography></Box>
                      </Stack>
                    </Paper>
                  </Grid>
                </Grid>
              </Stack>
            )}
          </Stack>
        </AccordionDetails>
      </Accordion>

      {/* Cache Stats Panel */}
      <CacheStatsPanel />
    </Box>
  );
}
