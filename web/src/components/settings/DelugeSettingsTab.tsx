// file: web/src/components/settings/DelugeSettingsTab.tsx
// version: 1.1.0
// guid: 4f2a3b1c-5d6e-4a70-b8c5-3d7e0f1b9a99

import { useCallback, useEffect, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  Chip,
  Divider,
  Grid,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from '@mui/material';

const API_BASE = '/api/v1';

interface DelugeStatus {
  configured: boolean;
  url: string;
}

interface TorrentInfo {
  hash: string;
  name: string;
  save_path: string;
  state: string;
  progress: number;
}

export default function DelugeSettingsTab() {
  const [status, setStatus] = useState<DelugeStatus | null>(null);
  const [testResult, setTestResult] = useState<{ connected: boolean; error?: string } | null>(null);
  const [testing, setTesting] = useState(false);
  const [torrents, setTorrents] = useState<Record<string, TorrentInfo>>({});
  const [showTorrents, setShowTorrents] = useState(false);
  const [importing, setImporting] = useState(false);
  const [importResult, setImportResult] = useState<{ total: number; imported: number; failed: number } | null>(null);

  useEffect(() => {
    fetch(`${API_BASE}/deluge/status`)
      .then((r) => r.json())
      .then(setStatus)
      .catch(() => {});
  }, []);

  const handleTestConnection = useCallback(async () => {
    setTesting(true);
    setTestResult(null);
    try {
      const resp = await fetch(`${API_BASE}/deluge/test-connection`, { method: 'POST' });
      const data = await resp.json();
      setTestResult(data);
    } catch (err: unknown) {
      setTestResult({ connected: false, error: (err as Error).message });
    } finally {
      setTesting(false);
    }
  }, []);

  const handleLoadTorrents = useCallback(async () => {
    try {
      const resp = await fetch(`${API_BASE}/deluge/torrents`);
      const data = await resp.json();
      setTorrents(data.torrents || {});
      setShowTorrents(true);
    } catch {
      setTorrents({});
    }
  }, []);

  const handleBulkImport = useCallback(async () => {
    setImporting(true);
    setImportResult(null);
    try {
      const resp = await fetch(`${API_BASE}/discovery/import`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ dry_run: false }),
      });
      const data = await resp.json();
      setImportResult({ total: data.total, imported: data.imported, failed: data.failed });
    } catch {
      setImportResult(null);
    } finally {
      setImporting(false);
    }
  }, []);

  return (
    <Box>
      <Typography variant="h6" gutterBottom>
        Deluge Integration
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Connect to your Deluge Web UI to automatically update torrent storage paths
        when books are reorganized, version-swapped, or undone.
      </Typography>
      <Divider sx={{ mb: 2 }} />

      <Grid container spacing={2}>
        <Grid item xs={12} sm={8}>
          <TextField
            fullWidth
            label="Deluge Web URL"
            placeholder="http://<deluge-host>:8112"
            value={status?.url || ''}
            disabled
            helperText="Set deluge_web_url in server config or environment. Default password: deluge"
            size="small"
          />
        </Grid>
        <Grid item xs={12} sm={4}>
          <Box sx={{ display: 'flex', gap: 1, alignItems: 'center', height: '100%' }}>
            {status?.configured ? (
              <Chip label="Configured" color="success" size="small" />
            ) : (
              <Chip label="Not Configured" color="default" size="small" />
            )}
          </Box>
        </Grid>
      </Grid>

      <Box sx={{ mt: 2, display: 'flex', gap: 1, alignItems: 'center' }}>
        <Button
          variant="outlined"
          onClick={handleTestConnection}
          disabled={!status?.configured || testing}
        >
          {testing ? 'Testing...' : 'Test Connection'}
        </Button>
        <Button
          variant="outlined"
          onClick={handleLoadTorrents}
          disabled={!status?.configured}
        >
          View Torrents
        </Button>
      </Box>

      {testResult && (
        <Alert severity={testResult.connected ? 'success' : 'error'} sx={{ mt: 2 }}>
          {testResult.connected
            ? 'Connected to Deluge successfully!'
            : `Connection failed: ${testResult.error || 'Unknown error'}`}
        </Alert>
      )}

      {showTorrents && (
        <Box sx={{ mt: 3 }}>
          <Typography variant="subtitle1" gutterBottom>
            Torrents ({Object.keys(torrents).length})
          </Typography>
          <TableContainer component={Paper} sx={{ maxHeight: 400 }}>
            <Table size="small" stickyHeader>
              <TableHead>
                <TableRow>
                  <TableCell>Name</TableCell>
                  <TableCell>State</TableCell>
                  <TableCell>Progress</TableCell>
                  <TableCell>Save Path</TableCell>
                  <TableCell>Hash</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {Object.entries(torrents).map(([id, t]) => (
                  <TableRow key={id}>
                    <TableCell>{t.name}</TableCell>
                    <TableCell>
                      <Chip
                        label={t.state}
                        size="small"
                        color={t.state === 'Seeding' ? 'success' : t.state === 'Downloading' ? 'primary' : 'default'}
                      />
                    </TableCell>
                    <TableCell>{Math.round(t.progress)}%</TableCell>
                    <TableCell>
                      <Typography variant="caption" fontFamily="monospace" noWrap>
                        {t.save_path}
                      </Typography>
                    </TableCell>
                    <TableCell>
                      <Typography variant="caption" fontFamily="monospace" noWrap>
                        {t.hash?.slice(0, 12)}...
                      </Typography>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableContainer>
        </Box>
      )}

      <Box sx={{ mt: 3 }}>
        <Typography variant="subtitle1" fontWeight={600} gutterBottom>
          Bulk Import
        </Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          Import all pending Deluge files (those with a torrent hash but not yet copied to the
          library).
        </Typography>
        <Button
          variant="outlined"
          onClick={handleBulkImport}
          disabled={importing}
        >
          {importing ? 'Importing…' : 'Import Unimported'}
        </Button>
        {importResult && (
          <Alert severity={importResult.failed > 0 ? 'warning' : 'success'} sx={{ mt: 2 }}>
            Total: {importResult.total} — Imported: {importResult.imported} — Failed:{' '}
            {importResult.failed}
          </Alert>
        )}
      </Box>
    </Box>
  );
}
