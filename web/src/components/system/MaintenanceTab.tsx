// file: web/src/components/system/MaintenanceTab.tsx
// version: 1.8.3
// guid: c3d4e5f6-a7b8-9012-cdef-345678901234
// last-edited: 2026-08-07

import { useEffect, useState, useCallback, useRef } from 'react';
import {
  Alert,
  Box,
  Button,
  Card,
  CardActions,
  CardContent,
  CardHeader,
  Chip,
  CircularProgress,
  Collapse,
  FormControlLabel,
  List,
  ListItem,
  ListItemText,
  Stack,
  Switch,
  TextField,
  Typography,
  useMediaQuery,
  useTheme,
} from '@mui/material';
import PlayArrowIcon from '@mui/icons-material/PlayArrow';
import * as api from '../../services/api';
import { withOptimisticOperation } from '../../utils/withOptimisticOperation';

// ─── OptimizeLibraryCard ────────────────────────────────────────────────────

function OptimizeLibraryCard() {
  const [optimizeRunning, setOptimizeRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [successMsg, setSuccessMsg] = useState<string | null>(null);
  const handleOptimize = async () => {
    setOptimizeRunning(true);
    setError(null);
    setSuccessMsg(null);
    try {
      const result = await api.startOptimize();
      setSuccessMsg(`Optimize started (op ${result.operation_id})`);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to start optimize');
    } finally {
      setOptimizeRunning(false);
    }
  };

  return (
    <Card sx={{ mb: 3, p: 2, borderLeft: '4px solid', borderColor: 'primary.main' }}>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <Box>
          <Typography variant="h6">⚡ Optimize Library</Typography>
          <Typography variant="body2" color="textSecondary">
            Run cleanup, fingerprint missing, dedup scan, and ID backfill in order.
          </Typography>
        </Box>
        <Button
          variant="contained"
          color="primary"
          onClick={handleOptimize}
          disabled={optimizeRunning}
        >
          {optimizeRunning ? 'Running...' : 'Run Optimize'}
        </Button>
      </Box>
      {error && (
        <Alert severity="error" sx={{ mt: 2 }} onClose={() => setError(null)}>
          {error}
        </Alert>
      )}
      {successMsg && (
        <Alert severity="success" sx={{ mt: 2 }} onClose={() => setSuccessMsg(null)}>
          {successMsg}
        </Alert>
      )}
    </Card>
  );
}

// ─── MaintenanceWindowCard ───────────────────────────────────────────────────

function MaintenanceWindowCard() {
  const [status, setStatus] = useState<api.MaintenanceWindowStatus | null>(null);
  const [config, setConfig] = useState<api.MaintenanceWindowConfig>({
    enabled: false,
    window_start: 1,
    window_end: 4,
  });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [successMsg, setSuccessMsg] = useState<string | null>(null);
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const isUnmountedRef = useRef(false);

  const loadStatus = useCallback(async () => {
    try {
      const s = await api.getMaintenanceWindowStatus();
      setStatus(s);
      setConfig({ enabled: s.enabled, window_start: s.window_start, window_end: s.window_end });
    } catch {
      // degrade silently if backend endpoint not yet deployed
    }
  }, []);

  useEffect(() => { loadStatus(); }, [loadStatus]);

  // Cleanup timeouts on unmount
  useEffect(() => {
    return () => {
      isUnmountedRef.current = true;
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
        timeoutRef.current = null;
      }
    };
  }, []);

  const handleSave = async () => {
    setSaving(true);
    setError(null);
    setSuccessMsg(null);
    try {
      await api.updateMaintenanceWindowConfig(config);
      setSuccessMsg('Maintenance window settings saved');
      await loadStatus();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to save');
    } finally {
      setSaving(false);
    }
  };

  const handleRunNow = async () => {
    setError(null);
    setSuccessMsg(null);
    try {
      await api.runMaintenanceWindow();
      setSuccessMsg('Maintenance window triggered');
      // Clear any existing timeout
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
      }
      // Schedule load with cleanup protection
      timeoutRef.current = setTimeout(() => {
        if (!isUnmountedRef.current) {
          loadStatus();
        }
        timeoutRef.current = null;
      }, 1000);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to trigger');
    }
  };

  const nextRunLabel = status?.next_run_estimate
    ? new Date(status.next_run_estimate).toLocaleString()
    : '—';
  const lastRunLabel = status?.last_run_date || 'Never';

  return (
    <Card sx={{ mb: 3 }}>
      <CardHeader
        title="Maintenance Window"
        subheader={`Last run: ${lastRunLabel} · Next run: ${nextRunLabel}`}
        action={status?.currently_running ? <CircularProgress size={20} sx={{ mt: 1, mr: 1 }} /> : null}
      />
      <CardContent>
        {error && (
          <Alert severity="error" sx={{ mb: 1.5 }} onClose={() => setError(null)}>
            {error}
          </Alert>
        )}
        {successMsg && (
          <Alert severity="success" sx={{ mb: 1.5 }} onClose={() => setSuccessMsg(null)}>
            {successMsg}
          </Alert>
        )}
        <Box sx={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 2 }}>
          <FormControlLabel
            control={
              <Switch
                checked={config.enabled}
                onChange={(e) => setConfig((c) => ({ ...c, enabled: e.target.checked }))}
              />
            }
            label="Enabled"
          />
          <TextField
            label="Start hour (0-23)"
            type="number"
            size="small"
            inputProps={{ min: 0, max: 23 }}
            value={config.window_start}
            onChange={(e) =>
              setConfig((c) => ({ ...c, window_start: Math.min(23, Math.max(0, parseInt(e.target.value) || 0)) }))
            }
            sx={{ width: 155 }}
          />
          <TextField
            label="End hour (0-23)"
            type="number"
            size="small"
            inputProps={{ min: 0, max: 23 }}
            value={config.window_end}
            onChange={(e) =>
              setConfig((c) => ({ ...c, window_end: Math.min(23, Math.max(0, parseInt(e.target.value) || 0)) }))
            }
            sx={{ width: 155 }}
          />
          <Button variant="outlined" onClick={handleSave} disabled={saving}>
            {saving ? 'Saving…' : 'Save'}
          </Button>
          <Button
            variant="contained"
            startIcon={<PlayArrowIcon />}
            onClick={handleRunNow}
            disabled={status?.currently_running ?? false}
          >
            Run Maintenance Now
          </Button>
        </Box>
      </CardContent>
    </Card>
  );
}

// ─── MaintenanceTab ──────────────────────────────────────────────────────────

const categoryLabels: Record<string, string> = {
  library: 'Library',
  sync: 'Sync',
  maintenance: 'Maintenance',
};
const categoryOrder = ['library', 'sync', 'maintenance'];

// ─── ChapterConsolidationCard ────────────────────────────────────────────────

function ChapterConsolidationCard() {
  const [scanning, setScanning] = useState(false);
  const [merging, setMerging] = useState(false);
  const [dryRun, setDryRun] = useState(true);
  const [scanResult, setScanResult] = useState<api.ChapterGroupsResult | null>(null);
  const [mergeResult, setMergeResult] = useState<api.ChapterMergeResult | null>(null);
  const [expanded, setExpanded] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleScan = useCallback(async () => {
    setScanning(true);
    setError(null);
    setScanResult(null);
    setMergeResult(null);
    try {
      const result = await api.scanChapterGroups();
      setScanResult(result);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Scan failed');
    } finally {
      setScanning(false);
    }
  }, []);

  const handleMerge = useCallback(async () => {
    setMerging(true);
    setError(null);
    setMergeResult(null);
    try {
      const result = await api.mergeChapterGroups({ dry_run: dryRun });
      setMergeResult(result);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Merge failed');
    } finally {
      setMerging(false);
    }
  }, [dryRun]);

  const groups = mergeResult?.groups ?? scanResult?.groups ?? [];

  return (
    <Card variant="outlined" sx={{ mb: 2 }}>
      <CardHeader
        title="Chapter Consolidation"
        subheader="Detect and merge sequential chapter files (01 - Title.mp3, 02 - Title.mp3 …) into a single book record"
      />
      <CardContent>
        {error && (
          <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>
            {error}
          </Alert>
        )}

        <Stack direction="row" spacing={2} flexWrap="wrap" sx={{ mb: 2 }}>
          <Button
            variant="outlined"
            startIcon={scanning ? <CircularProgress size={14} /> : undefined}
            disabled={scanning}
            onClick={handleScan}
          >
            {scanning ? 'Scanning…' : 'Scan for Chapter Groups'}
          </Button>

          <FormControlLabel
            control={<Switch checked={dryRun} onChange={(e) => setDryRun(e.target.checked)} size="small" />}
            label="Dry Run"
          />

          <Button
            variant="contained"
            color={dryRun ? 'info' : 'warning'}
            startIcon={merging ? <CircularProgress size={14} color="inherit" /> : undefined}
            disabled={merging}
            onClick={handleMerge}
          >
            {merging ? 'Merging…' : dryRun ? 'Preview Merge' : 'Merge Chapter Groups'}
          </Button>
        </Stack>

        {scanResult && !mergeResult && (
          <Typography variant="body2" sx={{ mb: 1 }}>
            Found <strong>{scanResult.groups.length}</strong> group(s) affecting{' '}
            <strong>{scanResult.total_books_affected}</strong> book record(s).
          </Typography>
        )}

        {mergeResult && (
          <Typography variant="body2" sx={{ mb: 1 }}>
            {mergeResult.dry_run ? '[Dry run] Would merge' : 'Merged'}{' '}
            <strong>{mergeResult.books_merged}</strong> book record(s) across{' '}
            <strong>{mergeResult.groups_found}</strong> group(s).
            {mergeResult.books_skipped > 0 && (
              <> Skipped <strong>{mergeResult.books_skipped}</strong>.</>
            )}
          </Typography>
        )}

        {groups.length > 0 && (
          <>
            <Button size="small" onClick={() => setExpanded((v) => !v)} sx={{ mb: 1 }}>
              {expanded ? 'Hide groups' : `Show ${groups.length} group(s)`}
            </Button>
            <Collapse in={expanded}>
              <List dense disablePadding>
                {groups.map((g) => (
                  <ListItem key={g.primary_book_id} disableGutters>
                    <ListItemText
                      primary={g.common_title || '(unknown title)'}
                      secondary={`${g.file_count} files · ${Math.round(g.total_duration / 60)} min total`}
                    />
                    <Chip size="small" label={`${g.file_count} files`} sx={{ ml: 1 }} />
                  </ListItem>
                ))}
              </List>
            </Collapse>
          </>
        )}
      </CardContent>
    </Card>
  );
}

// ─── SHADuplicateCard ─────────────────────────────────────────────────────────

function SHADuplicateCard() {
  const [scanning, setScanning] = useState(false);
  const [result, setResult] = useState<api.DuplicateFilesResult | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Hash coverage stats
  const [stats, setStats] = useState<api.BookFileHashStats | null>(null);
  const [statsLoading, setStatsLoading] = useState(false);
  const [backfilling, setBackfilling] = useState(false);
  const [backfillOpID, setBackfillOpID] = useState<string | null>(null);

  const loadStats = useCallback(async () => {
    setStatsLoading(true);
    try {
      setStats(await api.getBookFileHashStats());
    } catch {
      // non-fatal — stats may not be available yet
    } finally {
      setStatsLoading(false);
    }
  }, []);

  useEffect(() => { void loadStats(); }, [loadStats]);

  const handleScan = useCallback(async () => {
    setScanning(true);
    setError(null);
    setResult(null);
    try {
      const r = await api.scanDuplicateFiles(50);
      setResult(r);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Scan failed');
    } finally {
      setScanning(false);
    }
  }, []);

  const handleBackfill = useCallback(async () => {
    setBackfilling(true);
    setBackfillOpID(null);
    setError(null);
    try {
      const r = await api.backfillFileHashes(false);
      setBackfillOpID(r.operation_id);
      await loadStats();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Backfill failed');
    } finally {
      setBackfilling(false);
    }
  }, [loadStats]);

  const fmt = (bytes: number) => {
    if (bytes >= 1_073_741_824) return `${(bytes / 1_073_741_824).toFixed(1)} GB`;
    if (bytes >= 1_048_576) return `${(bytes / 1_048_576).toFixed(1)} MB`;
    return `${(bytes / 1024).toFixed(0)} KB`;
  };

  const pct = (n: number, d: number) => (d === 0 ? '—' : `${((n / d) * 100).toFixed(1)}%`);

  const statusChip = (() => {
    if (statsLoading) return <Chip size="small" label="Loading…" />;
    if (!stats) return null;
    if (stats.missing_file_hash === 0)
      return <Chip size="small" color="success" label="✓ All hashed" />;
    return <Chip size="small" color="warning" label={`${stats.missing_file_hash} missing hashes`} />;
  })();

  return (
    <Card variant="outlined" sx={{ mb: 2 }}>
      <CardHeader
        title="SHA Duplicate Detection"
        subheader="Files with identical SHA-256 content. Also tracks hash coverage for multi-file dedup."
        action={statusChip}
      />
      <CardContent>
        {error && (
          <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>
            {error}
          </Alert>
        )}

        {/* Hash coverage stats */}
        {stats && (
          <Box sx={{ mb: 2, p: 1.5, bgcolor: 'action.hover', borderRadius: 1 }}>
            <Typography variant="subtitle2" gutterBottom>Hash Coverage</Typography>
            <Stack direction="row" spacing={3} flexWrap="wrap" useFlexGap>
              <Box>
                <Typography variant="caption" color="text.secondary">Total files</Typography>
                <Typography variant="body2" fontWeight="bold">{stats.total_book_files.toLocaleString()}</Typography>
              </Box>
              <Box>
                <Typography variant="caption" color="text.secondary">With hash</Typography>
                <Typography variant="body2" fontWeight="bold">
                  {stats.with_file_hash.toLocaleString()} ({pct(stats.with_file_hash, stats.total_book_files)})
                </Typography>
              </Box>
              <Box>
                <Typography variant="caption" color="text.secondary">Missing hash</Typography>
                <Typography variant="body2" fontWeight="bold" color={stats.missing_file_hash > 0 ? 'warning.main' : 'success.main'}>
                  {stats.missing_file_hash.toLocaleString()}
                </Typography>
              </Box>
              <Box>
                <Typography variant="caption" color="text.secondary">With orig hash</Typography>
                <Typography variant="body2" fontWeight="bold">{stats.with_original_hash.toLocaleString()}</Typography>
              </Box>
              <Box>
                <Typography variant="caption" color="text.secondary">Books (no files)</Typography>
                <Typography variant="body2" fontWeight="bold" color={stats.books_with_no_files > 0 ? 'warning.main' : 'text.primary'}>
                  {stats.books_with_no_files.toLocaleString()} / {stats.total_books.toLocaleString()}
                </Typography>
              </Box>
            </Stack>

            {(stats.by_library?.length ?? 0) > 1 && (
              <Box sx={{ mt: 1.5 }}>
                <Typography variant="caption" color="text.secondary" display="block" gutterBottom>By Library</Typography>
                {stats.by_library.map((lib) => (
                  <Stack key={lib.path} direction="row" spacing={1} alignItems="center" sx={{ mb: 0.5 }}>
                    <Typography variant="caption" sx={{ minWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={lib.path}>
                      {lib.path.split('/').pop() || lib.path}
                    </Typography>
                    <Typography variant="caption" color="text.secondary">
                      {lib.with_hash}/{lib.total_files} hashed
                    </Typography>
                    {lib.missing_hash > 0 && (
                      <Chip size="small" label={`${lib.missing_hash} missing`} color="warning" variant="outlined" />
                    )}
                  </Stack>
                ))}
              </Box>
            )}
          </Box>
        )}

        <Stack direction="row" spacing={2} sx={{ mb: 2 }} flexWrap="wrap" useFlexGap>
          <Button
            variant="outlined"
            startIcon={scanning ? <CircularProgress size={14} /> : undefined}
            disabled={scanning}
            onClick={handleScan}
          >
            {scanning ? 'Scanning…' : 'Scan for SHA Duplicates'}
          </Button>
          <Button
            variant="outlined"
            color="secondary"
            startIcon={backfilling ? <CircularProgress size={14} /> : undefined}
            disabled={backfilling || statsLoading}
            onClick={handleBackfill}
          >
            {backfilling ? 'Backfilling…' : 'Backfill Missing Hashes'}
          </Button>
          <Button
            size="small"
            variant="text"
            disabled={statsLoading}
            onClick={() => void loadStats()}
          >
            Refresh Stats
          </Button>
        </Stack>

        {backfillOpID && (
          <Alert severity="info" sx={{ mb: 2 }} onClose={() => setBackfillOpID(null)}>
            Backfill job started — operation ID: <strong>{backfillOpID}</strong>
          </Alert>
        )}

        {result && (
          <Typography variant="body2" sx={{ mb: 1 }}>
            Found <strong>{result.total_groups}</strong> duplicate group(s) —{' '}
            <strong>{fmt(result.total_wasted_bytes)}</strong> wasted space.
          </Typography>
        )}

        {result && (result.groups?.length ?? 0) > 0 && (
          <List dense disablePadding>
            {(result.groups ?? []).map((g) => (
              <Box key={g.hash} sx={{ mb: 1 }}>
                <ListItem
                  disableGutters
                  sx={{ cursor: 'pointer' }}
                  onClick={() => setExpanded(expanded === g.hash ? null : g.hash)}
                >
                  <ListItemText
                    primary={`${g.files[0]?.book_title || '(unknown)'} — ${g.file_count} copies`}
                    secondary={`${fmt(g.total_size_bytes)} total · hash: ${g.hash.slice(0, 12)}…`}
                  />
                  <Chip size="small" label={`${g.file_count} files`} sx={{ ml: 1 }} />
                </ListItem>
                <Collapse in={expanded === g.hash}>
                  <List dense disablePadding sx={{ pl: 2 }}>
                    {g.files.map((f) => (
                      <ListItem key={f.book_file_id} disableGutters>
                        <ListItemText
                          primary={f.file_path || f.book_path}
                          secondary={f.book_title}
                        />
                        <Chip size="small" label={fmt(f.file_size_bytes)} variant="outlined" sx={{ ml: 1 }} />
                      </ListItem>
                    ))}
                  </List>
                </Collapse>
              </Box>
            ))}
          </List>
        )}
      </CardContent>
    </Card>
  );
}

// ─── AcoustIDFingerprintCard ──────────────────────────────────────────────────

function AcoustIDFingerprintCard() {
  const [stats, setStats] = useState<api.AcoustIDStats | null>(null);
  const [loading, setLoading] = useState(false);
  const [triggering, setTriggering] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const loadStats = useCallback(async () => {
    setLoading(true);
    try {
      setStats(await api.getAcoustIDStats());
    } catch {
      // degrade silently
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void loadStats(); }, [loadStats]);

  const handleFingerprint = useCallback(async () => {
    setTriggering(true);
    setError(null);
    try {
      await api.runMaintenanceJob('backfill-file-hashes', false);
      await loadStats();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to trigger fingerprint job');
    } finally {
      setTriggering(false);
    }
  }, [loadStats]);

  const pct = stats && stats.total_files > 0
    ? Math.round((stats.with_fingerprint / stats.total_files) * 100)
    : 0;

  return (
    <Card sx={{ mb: 2 }}>
      <CardHeader
        title="AcoustID Fingerprint Coverage"
        subheader="Files with at least one AcoustID fingerprint segment populated."
        action={loading ? <CircularProgress size={20} sx={{ mt: 1, mr: 1 }} /> : null}
      />
      <CardContent>
        {stats ? (
          <>
            <Typography variant="h4" gutterBottom>
              {pct}%
            </Typography>
            <Typography variant="body2" color="text.secondary" gutterBottom>
              {stats.with_fingerprint.toLocaleString()} / {stats.total_files.toLocaleString()} files fingerprinted
            </Typography>
            <Chip
              label={pct === 100 ? '✓ Complete' : 'Partial coverage'}
              color={pct === 100 ? 'success' : 'warning'}
              size="small"
            />
          </>
        ) : (
          !loading && <Typography variant="body2" color="text.secondary">No data available</Typography>
        )}
        {error && (
          <Alert severity="error" sx={{ mt: 1 }} onClose={() => setError(null)}>
            {error}
          </Alert>
        )}
      </CardContent>
      <CardActions>
        <Button
          onClick={handleFingerprint}
          disabled={triggering}
          variant="outlined"
          size="small"
          startIcon={triggering ? <CircularProgress size={14} /> : undefined}
        >
          {triggering ? 'Starting…' : 'Fingerprint Library'}
        </Button>
        <Button onClick={loadStats} size="small" disabled={loading}>
          Refresh
        </Button>
      </CardActions>
    </Card>
  );
}

// ─── MetadataHashDuplicateCard ────────────────────────────────────────────────

function MetadataHashDuplicateCard() {
  const [scanning, setScanning] = useState(false);
  const [result, setResult] = useState<api.MetadataHashDuplicatesResult | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  // NEW: stats + backfill
  const [stats, setStats] = useState<api.BookMetadataHashStats | null>(null);
  const [statsLoading, setStatsLoading] = useState(false);
  const [backfilling, setBackfilling] = useState(false);
  const [backfillOpID, setBackfillOpID] = useState<string | null>(null);

  const loadStats = useCallback(async () => {
    setStatsLoading(true);
    try {
      setStats(await api.getBookMetadataHashStats());
    } catch {
      // degrade silently if endpoint not available yet
    } finally {
      setStatsLoading(false);
    }
  }, []);

  useEffect(() => { void loadStats(); }, [loadStats]);

  const handleScan = useCallback(async () => {
    setScanning(true);
    setError(null);
    setResult(null);
    try {
      const r = await api.findMetadataHashDuplicates();
      setResult(r);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Scan failed');
    } finally {
      setScanning(false);
    }
  }, []);

  const handleBackfill = useCallback(async () => {
    setBackfilling(true);
    setError(null);
    setBackfillOpID(null);
    try {
      const r = await api.backfillMetadataHashes(false);
      // returns { operation_id }
      setBackfillOpID(r.operation_id ?? null);
      await loadStats();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Backfill failed');
    } finally {
      setBackfilling(false);
    }
  }, [loadStats]);

  const pct = (n: number, d: number) => (d === 0 ? '—' : `${((n / d) * 100).toFixed(1)}%`);

  const statusChip = (() => {
    if (statsLoading) return <Chip size="small" label="Loading…" />;
    if (!stats) return null;
    if (stats.missing_metadata_hash === 0)
      return <Chip size="small" color="success" label="✓ All hashed" />;
    return <Chip size="small" color="warning" label={`${stats.missing_metadata_hash} missing hashes`} />;
  })();

  return (
    <Card variant="outlined" sx={{ mb: 2 }}>
      <CardHeader
        title="Metadata Hash Duplicates"
        subheader="Books that share the same metadata source hash (same ASIN/ISBN from the same provider)."
        action={statusChip}
      />
      <CardContent>
        {error && (
          <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>
            {error}
          </Alert>
        )}

        {/* Stats panel */}
        {stats && (
          <Box sx={{ mb: 2, p: 1.5, bgcolor: 'action.hover', borderRadius: 1 }}>
            <Typography variant="subtitle2" gutterBottom>Metadata Hash Coverage</Typography>
            <Stack direction="row" spacing={3} flexWrap="wrap" useFlexGap>
              <Box>
                <Typography variant="caption" color="text.secondary">Total books</Typography>
                <Typography variant="body2" fontWeight="bold">{stats.total_books.toLocaleString()}</Typography>
              </Box>
              <Box>
                <Typography variant="caption" color="text.secondary">With metadata hash</Typography>
                <Typography variant="body2" fontWeight="bold">{stats.with_metadata_hash.toLocaleString()} ({pct(stats.with_metadata_hash, stats.total_books)})</Typography>
              </Box>
              <Box>
                <Typography variant="caption" color="text.secondary">Missing metadata hash</Typography>
                <Typography variant="body2" fontWeight="bold" color={stats.missing_metadata_hash > 0 ? 'warning.main' : 'success.main'}>
                  {stats.missing_metadata_hash.toLocaleString()}
                </Typography>
              </Box>
              <Box>
                <Typography variant="caption" color="text.secondary">Has ASIN/ISBN</Typography>
                <Typography variant="body2" fontWeight="bold">{stats.with_asin_or_isbn.toLocaleString()}</Typography>
              </Box>
              <Box>
                <Typography variant="caption" color="text.secondary">Missing hash but has ID</Typography>
                <Typography variant="body2" fontWeight="bold">{stats.missing_hash_has_id.toLocaleString()}</Typography>
              </Box>
            </Stack>

            {(stats.by_library?.length ?? 0) > 1 && (
              <Box sx={{ mt: 1.5 }}>
                <Typography variant="caption" color="text.secondary" display="block" gutterBottom>By Library</Typography>
                {stats.by_library.map((lib) => (
                  <Stack key={lib.path} direction="row" spacing={1} alignItems="center" sx={{ mb: 0.5 }}>
                    <Typography variant="caption" sx={{ minWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={lib.path}>
                      {lib.path.split('/').pop() || lib.path}
                    </Typography>
                    <Typography variant="caption" color="text.secondary">
                      {lib.with_metadata_hash}/{lib.total_books} hashed
                    </Typography>
                    {lib.missing_metadata_hash > 0 && (
                      <Chip size="small" label={`${lib.missing_metadata_hash} missing`} color="warning" variant="outlined" />
                    )}
                  </Stack>
                ))}
              </Box>
            )}
          </Box>
        )}

        <Stack direction="row" spacing={2} sx={{ mb: 2 }}>
          <Button
            variant="outlined"
            startIcon={scanning ? <CircularProgress size={14} /> : undefined}
            disabled={scanning}
            onClick={handleScan}
          >
            {scanning ? 'Scanning…' : 'Scan for Metadata Hash Duplicates'}
          </Button>

          <Button
            variant="outlined"
            color="secondary"
            startIcon={backfilling ? <CircularProgress size={14} /> : undefined}
            disabled={backfilling || statsLoading}
            onClick={handleBackfill}
          >
            {backfilling ? 'Starting…' : 'Backfill Missing Hashes'}
          </Button>

          <Button
            size="small"
            variant="text"
            disabled={statsLoading}
            onClick={() => void loadStats()}
          >
            Refresh Stats
          </Button>
        </Stack>

        {backfillOpID && (
          <Alert severity="info" sx={{ mb: 2 }} onClose={() => setBackfillOpID(null)}>
            Backfill job started — operation ID: <strong>{backfillOpID}</strong>
          </Alert>
        )}

        {result && result.groups.length > 0 && (
          <List dense disablePadding>
            {result.groups.map((g) => (
              <Box key={g.hash} sx={{ mb: 1 }}>
                <ListItem
                  disableGutters
                  sx={{ cursor: 'pointer' }}
                  onClick={() => setExpanded(expanded === g.hash ? null : g.hash)}
                >
                  <ListItemText
                    primary={`${g.books[0]?.title || '(unknown)'} — ${g.books.length} copies`}
                    secondary={`hash: ${g.hash.slice(0, 16)}…`}
                  />
                  <Chip size="small" label={`${g.books.length} books`} sx={{ ml: 1 }} />
                </ListItem>
                <Collapse in={expanded === g.hash}>
                  <List dense disablePadding sx={{ pl: 2 }}>
                    {g.books.map((b) => (
                      <ListItem key={b.id} disableGutters>
                        <ListItemText
                          primary={b.title}
                          secondary={`ID: ${b.id}`}
                        />
                        <Chip size="small" label={`${b.file_count} files`} variant="outlined" sx={{ ml: 1 }} />
                      </ListItem>
                    ))}
                  </List>
                </Collapse>
              </Box>
            ))}
          </List>
        )}
      </CardContent>
    </Card>
  );
}

// ─── ManualFixesCard ──────────────────────────────────────────────────────────

function ManualFixesCard() {
  const [jobs, setJobs] = useState<api.MaintenanceJobDef[]>([]);
  const [loading, setLoading] = useState(true);
  const [running, setRunning] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    api.listMaintenanceJobs()
      .then(setJobs)
      .catch((e) => setError(e instanceof Error ? e.message : 'Failed to load jobs'))
      .finally(() => setLoading(false));
  }, []);

  const handleRun = async (jobId: string) => {
    setRunning(jobId);
    setError(null);
    try {
      // Bell + toast appear instantly; reconcile to the real op id once the
      // dispatcher returns.
      await withOptimisticOperation(`maintenance:${jobId}`, () => api.runMaintenanceJob(jobId));
    } catch (e) {
      setError(e instanceof Error ? e.message : `Failed to run ${jobId}`);
    } finally {
      setRunning(null);
    }
  };

  return (
    <Card sx={{ mb: 3 }}>
      <CardHeader title="Manual Fixes" subheader="One-shot maintenance jobs dispatched as async operations" />
      <CardContent>
        {error && (
          <Alert severity="error" sx={{ mb: 1.5 }} onClose={() => setError(null)}>
            {error}
          </Alert>
        )}
        {loading ? (
          <CircularProgress size={24} />
        ) : (
          <Stack spacing={1}>
            {jobs.map((job) => (
              <Box key={job.id} sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap' }}>
                <Box sx={{ flex: 1 }}>
                  <Typography variant="body2" fontWeight="medium">{job.id}</Typography>
                  <Typography variant="caption" color="text.secondary">{job.description}</Typography>
                </Box>
                <Button
                  variant="outlined"
                  size="small"
                  startIcon={running === job.id ? <CircularProgress size={14} color="inherit" /> : <PlayArrowIcon />}
                  disabled={running !== null}
                  onClick={() => handleRun(job.id)}
                >
                  {running === job.id ? 'Starting…' : 'Run'}
                </Button>
              </Box>
            ))}
          </Stack>
        )}
      </CardContent>
    </Card>
  );
}

// ─── MaintenanceTab ───────────────────────────────────────────────────────────

export function MaintenanceTab() {
  const theme = useTheme();
  const isMobile = useMediaQuery(theme.breakpoints.down('sm'));
  const [tasks, setTasks] = useState<api.TaskInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [runningTask, setRunningTask] = useState<string | null>(null);
  const [successMsg, setSuccessMsg] = useState<string | null>(null);
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const isUnmountedRef = useRef(false);

  const fetchTasks = useCallback(async () => {
    try {
      const data = await api.getRegisteredTasks();
      setTasks(data);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load tasks');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchTasks();
    intervalRef.current = setInterval(fetchTasks, 10000);
    return () => {
      if (intervalRef.current) {
        clearInterval(intervalRef.current);
        intervalRef.current = null;
      }
    };
  }, [fetchTasks]);

  // Cleanup timeouts on unmount
  useEffect(() => {
    return () => {
      isUnmountedRef.current = true;
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
        timeoutRef.current = null;
      }
    };
  }, []);

  const handleRun = async (name: string) => {
    setRunningTask(name);
    setSuccessMsg(null);
    try {
      await api.runTask(name);
      setSuccessMsg(`Task "${name}" started`);
      // Clear any existing timeout
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
      }
      // Schedule fetch with cleanup protection
      timeoutRef.current = setTimeout(() => {
        if (!isUnmountedRef.current) {
          fetchTasks();
        }
        timeoutRef.current = null;
      }, 1000);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to run task');
    } finally {
      setRunningTask(null);
    }
  };

  const handleToggle = async (name: string, enabled: boolean) => {
    try {
      await api.updateTaskConfig(name, { enabled });
      fetchTasks();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update task');
    }
  };

  const handleIntervalChange = async (name: string, minutes: number) => {
    try {
      await api.updateTaskConfig(name, { interval_minutes: minutes });
      fetchTasks();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update interval');
    }
  };

  const handleStartupToggle = async (name: string, runOnStartup: boolean) => {
    try {
      await api.updateTaskConfig(name, { run_on_startup: runOnStartup });
      fetchTasks();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update startup setting');
    }
  };

  const handleMaintenanceWindowToggle = async (name: string, runInMaintenanceWindow: boolean) => {
    try {
      await api.updateTaskConfig(name, { run_in_maintenance_window: runInMaintenanceWindow });
      fetchTasks();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update maintenance window setting');
    }
  };

  const grouped = categoryOrder
    .map((cat) => ({
      category: cat,
      label: categoryLabels[cat] || cat,
      tasks: tasks.filter((t) => t.category === cat),
    }))
    .filter((g) => g.tasks.length > 0);

  return (
    <Box>
      <OptimizeLibraryCard />

      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Configure and manually trigger background tasks. Tasks marked "Maint. Window" run
        automatically during the scheduled maintenance window.
      </Typography>

      <MaintenanceWindowCard />

      <ManualFixesCard />

      <ChapterConsolidationCard />

      <SHADuplicateCard />

      <AcoustIDFingerprintCard />

      <MetadataHashDuplicateCard />

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

      {loading ? (
        <Typography>Loading tasks…</Typography>
      ) : (
        <Stack spacing={4}>
          {grouped.map((group) => (
            <Box key={group.category}>
              <Typography variant="h6" sx={{ mb: 1 }}>
                {group.label}
              </Typography>
              <Stack spacing={1}>
                {group.tasks.map((task) => (
                  <Card key={task.name} variant="outlined">
                    <CardContent sx={{ py: 1.5, '&:last-child': { pb: 1.5 } }}>
                      <Box sx={{ mb: 0.5, display: 'flex', alignItems: 'center', gap: 1 }}>
                        {task.is_running && <CircularProgress size={14} />}
                        <Typography variant="subtitle2" sx={{ flexGrow: 1 }}>
                          {task.description}
                        </Typography>
                      </Box>
                      <Typography variant="caption" color="text.secondary">
                        {task.name}
                        {task.last_run && ` · Last run: ${new Date(task.last_run).toLocaleString()}`}
                      </Typography>

                      <Box
                        sx={{
                          display: 'flex',
                          flexWrap: 'wrap',
                          alignItems: 'center',
                          gap: 1,
                          mt: isMobile ? 1 : 0.5,
                        }}
                      >
                        <FormControlLabel
                          control={
                            <Switch
                              size="small"
                              checked={task.enabled}
                              onChange={(e) => handleToggle(task.name, e.target.checked)}
                            />
                          }
                          label="Enabled"
                          sx={{ mx: 0 }}
                        />

                        {task.interval_minutes > 0 && (
                          <TextField
                            label="Interval (min)"
                            type="number"
                            size="small"
                            value={task.interval_minutes}
                            onChange={(e) => {
                              const val = parseInt(e.target.value, 10);
                              if (val > 0) handleIntervalChange(task.name, val);
                            }}
                            sx={{ width: 120 }}
                            inputProps={{ min: 1 }}
                          />
                        )}

                        <FormControlLabel
                          control={
                            <Switch
                              size="small"
                              checked={task.run_on_startup}
                              onChange={(e) => handleStartupToggle(task.name, e.target.checked)}
                            />
                          }
                          label="On Start"
                          sx={{ mx: 0 }}
                        />

                        <FormControlLabel
                          control={
                            <Switch
                              size="small"
                              checked={task.run_in_maintenance_window}
                              onChange={(e) =>
                                handleMaintenanceWindowToggle(task.name, e.target.checked)
                              }
                            />
                          }
                          label="Maint. Window"
                          sx={{ mx: 0 }}
                        />

                        <Chip
                          size="small"
                          label={task.enabled ? 'Active' : 'Disabled'}
                          color={task.enabled ? 'success' : 'default'}
                          variant="outlined"
                        />

                        <Button
                          variant="contained"
                          size="small"
                          fullWidth={isMobile}
                          startIcon={
                            task.is_running
                              ? <CircularProgress size={14} color="inherit" />
                              : <PlayArrowIcon />
                          }
                          disabled={runningTask === task.name || task.is_running}
                          onClick={() => handleRun(task.name)}
                        >
                          {runningTask === task.name ? 'Starting…' : 'Run Now'}
                        </Button>
                      </Box>
                    </CardContent>
                  </Card>
                ))}
              </Stack>
            </Box>
          ))}
        </Stack>
      )}
    </Box>
  );
}
