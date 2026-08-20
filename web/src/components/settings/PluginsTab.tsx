// file: web/src/components/settings/PluginsTab.tsx
// version: 1.0.1
// guid: d5e6f7a8-b9c0-1d2e-3f4a-5b6c7d8e9f0a

import { useEffect, useState, useCallback } from 'react';
import {
  Box,
  Typography,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Chip,
  Button,
  CircularProgress,
  Alert,
  Stack,
  Collapse,
  IconButton,
  TextField,
  Divider,
} from '@mui/material';
import {
  ExpandMore as ExpandMoreIcon,
  ExpandLess as ExpandLessIcon,
  CheckCircle as CheckCircleIcon,
  Error as ErrorIcon,
  Refresh as RefreshIcon,
} from '@mui/icons-material';

interface PluginInfo {
  id: string;
  name: string;
  version: string;
  capabilities: string[];
  enabled: boolean;
  initialized: boolean;
  health: string;
}

interface PluginSettings {
  [key: string]: string;
}

async function fetchPlugins(): Promise<PluginInfo[]> {
  const resp = await fetch('/api/v1/plugins');
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
  const body = await resp.json();
  return body.data.plugins ?? [];
}

async function togglePlugin(id: string, enable: boolean): Promise<void> {
  const action = enable ? 'enable' : 'disable';
  const resp = await fetch(`/api/v1/plugins/${id}/${action}`, { method: 'POST' });
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
}

async function saveSettings(id: string, settings: PluginSettings): Promise<void> {
  const resp = await fetch(`/api/v1/plugins/${id}/settings`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(settings),
  });
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
}

function HealthBadge({ health }: { health: string }) {
  if (health === 'ok') {
    return (
      <Chip
        icon={<CheckCircleIcon />}
        label="Healthy"
        color="success"
        size="small"
        variant="outlined"
      />
    );
  }
  return (
    <Chip
      icon={<ErrorIcon />}
      label={health || 'Unknown'}
      color="error"
      size="small"
      variant="outlined"
      sx={{ maxWidth: 220, '& .MuiChip-label': { overflow: 'hidden', textOverflow: 'ellipsis' } }}
      title={health}
    />
  );
}

function PluginRow({ plugin: p, onRefresh }: { plugin: PluginInfo; onRefresh: () => void }) {
  const [expanded, setExpanded] = useState(false);
  const [saving, setSaving] = useState(false);
  const [settings, setSettings] = useState<PluginSettings>({});
  const [error, setError] = useState('');

  const handleToggle = async () => {
    setSaving(true);
    setError('');
    try {
      await togglePlugin(p.id, !p.enabled);
      onRefresh();
    } catch (e: unknown) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  };

  const handleSaveSettings = async () => {
    setSaving(true);
    setError('');
    try {
      await saveSettings(p.id, settings);
      onRefresh();
    } catch (e: unknown) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <TableRow hover>
        <TableCell>
          <Stack direction="row" alignItems="center" spacing={1}>
            <Typography variant="body2" fontWeight="medium">
              {p.name}
            </Typography>
            <Typography variant="caption" color="text.secondary">
              v{p.version}
            </Typography>
          </Stack>
          <Typography variant="caption" color="text.secondary">
            {p.id}
          </Typography>
        </TableCell>
        <TableCell>
          <Stack direction="row" spacing={0.5} flexWrap="wrap">
            {p.capabilities.map((cap) => (
              <Chip key={cap} label={cap} size="small" />
            ))}
          </Stack>
        </TableCell>
        <TableCell>
          <HealthBadge health={p.health} />
        </TableCell>
        <TableCell>
          <Stack direction="row" spacing={1} alignItems="center">
            <Button
              size="small"
              variant={p.enabled ? 'outlined' : 'contained'}
              color={p.enabled ? 'error' : 'success'}
              onClick={handleToggle}
              disabled={saving}
            >
              {p.enabled ? 'Disable' : 'Enable'}
            </Button>
            <IconButton size="small" onClick={() => setExpanded((e) => !e)}>
              {expanded ? <ExpandLessIcon /> : <ExpandMoreIcon />}
            </IconButton>
          </Stack>
        </TableCell>
      </TableRow>
      <TableRow>
        <TableCell colSpan={4} sx={{ py: 0 }}>
          <Collapse in={expanded} timeout="auto" unmountOnExit>
            <Box sx={{ p: 2 }}>
              <Typography variant="subtitle2" gutterBottom>
                Plugin Settings
              </Typography>
              <Divider sx={{ mb: 2 }} />
              {error && (
                <Alert severity="error" sx={{ mb: 2 }}>
                  {error}
                </Alert>
              )}
              <Stack spacing={1.5}>
                {Object.entries(settings).map(([k, v]) => (
                  <TextField
                    key={k}
                    label={k}
                    value={v}
                    size="small"
                    onChange={(e) => setSettings((prev) => ({ ...prev, [k]: e.target.value }))}
                  />
                ))}
                <Stack direction="row" spacing={1}>
                  <Button
                    size="small"
                    variant="outlined"
                    onClick={() => setSettings((prev) => ({ ...prev, '': '' }))}
                  >
                    Add Setting
                  </Button>
                  <Button
                    size="small"
                    variant="contained"
                    disabled={saving}
                    onClick={handleSaveSettings}
                  >
                    Save
                  </Button>
                </Stack>
              </Stack>
            </Box>
          </Collapse>
        </TableCell>
      </TableRow>
    </>
  );
}

export default function PluginsTab() {
  const [plugins, setPlugins] = useState<PluginInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      setPlugins(await fetchPlugins());
    } catch (e: unknown) {
      setError(String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  return (
    <Box>
      <Stack direction="row" alignItems="center" justifyContent="space-between" mb={2}>
        <Typography variant="h6">Plugins</Typography>
        <IconButton onClick={load} disabled={loading} title="Refresh">
          <RefreshIcon />
        </IconButton>
      </Stack>

      {error && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {error}
        </Alert>
      )}

      {loading ? (
        <Box display="flex" justifyContent="center" py={4}>
          <CircularProgress />
        </Box>
      ) : plugins.length === 0 ? (
        <Alert severity="info">No plugins registered.</Alert>
      ) : (
        <TableContainer component={Paper} variant="outlined">
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Plugin</TableCell>
                <TableCell>Capabilities</TableCell>
                <TableCell>Health</TableCell>
                <TableCell>Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {plugins.map((p) => (
                <PluginRow key={p.id} plugin={p} onRefresh={load} />
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Box>
  );
}
