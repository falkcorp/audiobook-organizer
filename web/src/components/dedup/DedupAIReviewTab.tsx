// file: web/src/components/dedup/DedupAIReviewTab.tsx
// version: 1.0.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-06-22

import { useState, useEffect } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useAsyncAction } from '../../hooks/useAsyncAction';
import {
  Box,
  Typography,
  Paper,
  Button,
  Alert,
  Chip,
  CircularProgress,
  Divider,
  Tab,
  Tabs,
  FormControlLabel,
  Switch,
  Card,
  CardContent,
  LinearProgress,
  Checkbox,
  Drawer,
} from '@mui/material';
import AutoAwesomeIcon from '@mui/icons-material/AutoAwesome';
import PersonIcon from '@mui/icons-material/Person';
import MenuBookIcon from '@mui/icons-material/MenuBook';
import * as api from '../../services/api';
import { RoleDetails } from './DedupAuthorTab';

// ---- AI Author Sub-Page (self-contained per mode) ----
// ---- AI Author Pipeline Page (unified scan-based view) ----
function AIAuthorPipelinePage() {
  const [scan, setScan] = useState<api.AIScanDetail | null>(null);
  const [results, setResults] = useState<api.AIScanResult[]>([]);
  const [scans, setScans] = useState<api.AIScan[]>([]);
  const [batchMode, setBatchMode] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [agreementFilter, setAgreementFilter] = useState<string>('all');
  const [error, setError] = useState<string | null>(null);

  const { loading, run: startScanAction } = useAsyncAction(async () => {
    setError(null);
    const newScan = await api.startAIScan(batchMode ? 'batch' : 'realtime');
    const detail = await api.getAIScan(newScan.id);
    setScan(detail);
    // Refresh scan list
    api.listAIScans().then(setScans).catch(() => {});
  });

  const startScan = async () => {
    await startScanAction();
  };

  // Load scan list on mount
  useEffect(() => {
    api.listAIScans().then(setScans).catch(() => {});
  }, []);

  // Poll active scan status
  useEffect(() => {
    if (!scan || scan.status === 'complete' || scan.status === 'failed') return;
    let mounted = true;
    const interval = setInterval(async () => {
      try {
        const updated = await api.getAIScan(scan.id);
        if (mounted) {
          setScan(updated);
          if (updated.status === 'complete') {
            const res = await api.getAIScanResults(scan.id);
            if (mounted) setResults(res);
            clearInterval(interval);
          }
        }
      } catch { /* ignore polling errors */ }
    }, 5000);
    return () => {
      mounted = false;
      clearInterval(interval);
    };
    // scan?.id and scan?.status are the meaningful change signals; including
    // the full `scan` object would restart the interval on every poll update.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scan?.id, scan?.status]);

  const { run: loadScanAction } = useAsyncAction(async (...args: unknown[]) => {
    const scanId = args[0] as number;
    const detail = await api.getAIScan(scanId);
    setScan(detail);
    if (detail.status === 'complete') {
      const res = await api.getAIScanResults(scanId);
      setResults(res);
    }
  });

  const loadScan = async (scanId: number) => {
    await loadScanAction(scanId);
  };

  const applySelected = async () => {
    if (!scan || selected.size === 0) return;
    try {
      await api.applyAIScanResults(scan.id, Array.from(selected));
      const res = await api.getAIScanResults(scan.id);
      setResults(res);
      setSelected(new Set());
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Failed to apply results');
    }
  };

  const filteredResults = agreementFilter === 'all'
    ? results
    : results.filter(r => r.agreement === agreementFilter);

  const toggleSelect = (id: number) => {
    setSelected(prev => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  };

  return (
    <Box>
      {/* Header Bar */}
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 2, p: 2 }}>
        <Button
          variant="contained"
          onClick={startScan}
          disabled={loading || (scan != null && scan.status !== 'complete' && scan.status !== 'failed')}
          startIcon={<AutoAwesomeIcon />}
        >
          Run Scan
        </Button>
        <FormControlLabel
          control={<Switch checked={batchMode} onChange={(_, v) => setBatchMode(v)} />}
          label={batchMode ? 'Batch (cheaper, hours)' : 'Realtime (faster, more expensive)'}
        />
        <Box sx={{ flex: 1 }} />
        <Button variant="outlined" onClick={() => setHistoryOpen(true)}>
          Scan History
        </Button>
      </Box>

      {error && (
        <Alert severity="error" sx={{ mx: 2 }} onClose={() => setError(null)}>
          {error}
        </Alert>
      )}

      {/* Active Scan Status */}
      {scan && scan.status !== 'complete' && scan.status !== 'failed' && scan.status !== 'canceled' && (
        <Paper
          elevation={3}
          sx={{
            position: 'sticky',
            top: 0,
            zIndex: 10,
            mx: 2,
            mb: 2,
            p: 2,
            borderRadius: 2,
          }}
        >
          <Box sx={{ display: 'flex', alignItems: 'center', gap: 2 }}>
            <Typography variant="subtitle2">Scan #{scan.id} — {scan.status}</Typography>
            <Box sx={{ display: 'flex', gap: 1 }}>
              {(scan.phases || []).map(phase => (
                <Chip
                  key={phase.phase_type}
                  label={`${phase.phase_type.replace('_', ' ')}: ${phase.status}`}
                  color={phase.status === 'complete' ? 'success' : phase.status === 'failed' ? 'error' : 'default'}
                  size="small"
                />
              ))}
            </Box>
            <Box sx={{ flex: 1 }} />
            <Button
              variant="outlined"
              color="error"
              size="small"
              onClick={async () => {
                try {
                  await api.cancelAIScan(scan.id);
                  const updated = await api.getAIScan(scan.id);
                  setScan(updated);
                } catch (e: unknown) {
                  setError(e instanceof Error ? e.message : 'Failed to cancel scan');
                }
              }}
            >
              Cancel Scan
            </Button>
          </Box>
          <LinearProgress sx={{ mt: 1 }} />
        </Paper>
      )}

      {/* Canceled scan message */}
      {scan && scan.status === 'canceled' && (
        <Alert severity="warning" sx={{ mx: 2, mb: 2 }}>
          Scan #{scan.id} was canceled.
        </Alert>
      )}

      {/* No scan loaded */}
      {!scan && !loading && (
        <Paper sx={{ p: 4, mx: 2, textAlign: 'center' }}>
          <Typography variant="body1" color="text.secondary">
            Run a scan to discover author duplicates using multi-pass AI analysis, or load a previous scan from history.
          </Typography>
        </Paper>
      )}

      {loading && !scan && (
        <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}><CircularProgress /></Box>
      )}

      {/* Scan failed */}
      {scan?.status === 'failed' && (
        <Alert severity="error" sx={{ mx: 2 }}>
          Scan #{scan.id} failed. Try running a new scan.
        </Alert>
      )}

      {/* Results */}
      {scan?.status === 'complete' && results.length > 0 && (
        <Box sx={{ px: 2 }}>
          {/* Filter Tabs */}
          <Tabs value={agreementFilter} onChange={(_, v) => setAgreementFilter(v)} sx={{ mb: 2 }}>
            <Tab value="all" label={`All (${results.length})`} />
            <Tab value="agreed" label={`Agreed (${results.filter(r => r.agreement === 'agreed').length})`} />
            <Tab value="groups_only" label={`Groups Only (${results.filter(r => r.agreement === 'groups_only').length})`} />
            <Tab value="full_only" label={`Full Only (${results.filter(r => r.agreement === 'full_only').length})`} />
            <Tab value="disagreed" label={`Disagreed (${results.filter(r => r.agreement === 'disagreed').length})`} />
          </Tabs>

          {/* Floating Apply Bar */}
          {selected.size > 0 && (
            <Paper
              elevation={4}
              sx={{
                position: 'sticky',
                bottom: 16,
                zIndex: 10,
                p: 1.5,
                mx: -2,
                mb: 2,
                display: 'flex',
                alignItems: 'center',
                gap: 2,
                borderRadius: 2,
                bgcolor: 'background.paper',
              }}
            >
              <Button variant="contained" color="primary" onClick={applySelected}>
                Apply Selected ({selected.size})
              </Button>
              <Button variant="outlined" size="small" onClick={() => setSelected(new Set())}>
                Clear Selection
              </Button>
              <Typography variant="body2" color="text.secondary" sx={{ ml: 'auto' }}>
                {selected.size} of {filteredResults.filter(r => !r.applied).length} selected
              </Typography>
            </Paper>
          )}

          {/* Result Cards */}
          {filteredResults.map(result => (
            <Card key={result.id} sx={{ mb: 1, opacity: result.applied ? 0.5 : 1 }} variant="outlined">
              <CardContent sx={{ py: 1, '&:last-child': { pb: 1 } }}>
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                  <Checkbox
                    checked={selected.has(result.id)}
                    onChange={() => toggleSelect(result.id)}
                    disabled={result.applied}
                    size="small"
                  />
                  <Chip
                    label={result.agreement}
                    size="small"
                    color={result.agreement === 'agreed' ? 'success' : result.agreement === 'disagreed' ? 'error' : 'default'}
                  />
                  <Chip label={result.suggestion.action} size="small" variant="outlined"
                    color={result.suggestion.action === 'merge' ? 'primary' : result.suggestion.action === 'rename' ? 'warning' : result.suggestion.action === 'alias' ? 'info' : 'default'} />
                  <Chip label={result.suggestion.confidence} size="small" variant="outlined"
                    color={result.suggestion.confidence === 'high' ? 'success' : result.suggestion.confidence === 'medium' ? 'warning' : 'error'} />
                  <Box sx={{ flex: 1 }}>
                    <Typography variant="body2" fontWeight="bold">
                      {result.suggestion.canonical_name}
                    </Typography>
                    <Typography variant="caption" color="text.secondary">
                      {result.suggestion.reason}
                    </Typography>
                  </Box>
                  {result.applied && <Chip label="Applied" size="small" color="info" />}
                </Box>
                {result.suggestion.roles && (
                  <>
                    <Divider sx={{ my: 0.5, ml: 5 }} />
                    <RoleDetails roles={result.suggestion.roles} />
                  </>
                )}
              </CardContent>
            </Card>
          ))}

          {/* No results for this filter */}
          {filteredResults.length === 0 && (
            <Typography color="text.secondary" sx={{ p: 2, textAlign: 'center' }}>
              No results matching this filter.
            </Typography>
          )}
        </Box>
      )}

      {/* Scan complete but no results */}
      {scan?.status === 'complete' && results.length === 0 && (
        <Paper sx={{ p: 4, mx: 2, textAlign: 'center' }}>
          <Typography variant="body1" color="text.secondary">
            Scan complete — no duplicate authors found.
          </Typography>
        </Paper>
      )}

      {/* Scan History Drawer */}
      <Drawer anchor="right" open={historyOpen} onClose={() => setHistoryOpen(false)}>
        <Box sx={{ width: 400, p: 2 }}>
          <Typography variant="h6" gutterBottom>Scan History</Typography>
          {scans.map(s => (
            <Card
              key={s.id}
              sx={{ mb: 1, cursor: 'pointer', border: scan?.id === s.id ? 2 : undefined, borderColor: scan?.id === s.id ? 'primary.main' : undefined }}
              variant="outlined"
              onClick={() => { loadScan(s.id); setHistoryOpen(false); }}
            >
              <CardContent sx={{ py: 1, '&:last-child': { pb: 1 } }}>
                <Typography variant="body2" fontWeight="bold">
                  Scan #{s.id} — {s.status}
                </Typography>
                <Typography variant="caption" color="text.secondary">
                  {new Date(s.created_at).toLocaleString()} · {s.author_count} authors · {s.mode}
                </Typography>
              </CardContent>
            </Card>
          ))}
          {scans.length === 0 && (
            <Typography color="text.secondary">No scan history yet.</Typography>
          )}
        </Box>
      </Drawer>
    </Box>
  );
}

// ---- AI Review Top-Level Tab ----
export function AIReviewTab() {
  const [searchParams, setSearchParams] = useSearchParams();
  const aiSub = searchParams.get('aisub') || 'authors';
  const setAiSub = (v: string) => {
    const next = new URLSearchParams(searchParams);
    next.set('aisub', v);
    setSearchParams(next, { replace: true });
  };

  return (
    <Box>
      <Tabs value={aiSub} onChange={(_, v) => setAiSub(v)} sx={{ mb: 2, borderBottom: 1, borderColor: 'divider' }}>
        <Tab value="authors" label="Authors" icon={<PersonIcon />} iconPosition="start" />
        <Tab value="books" label="Books" icon={<MenuBookIcon />} iconPosition="start" />
      </Tabs>

      {aiSub === 'authors' && <AIAuthorPipelinePage />}
      {aiSub === 'books' && (
        <Box sx={{ p: 4, textAlign: 'center' }}>
          <Typography color="text.secondary">Book deduplication coming soon.</Typography>
        </Box>
      )}
    </Box>
  );
}
