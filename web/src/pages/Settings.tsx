// file: web/src/pages/Settings.tsx
// version: 1.47.0
// guid: 7a8b9c0d-1e2f-3a4b-5c6d-7e8f9a0b1c2d
// last-edited: 2026-06-19

import { useState, useEffect, useMemo, useRef, ChangeEvent } from 'react';
import { useNavigate, useLocation } from 'react-router-dom';
import { useUnsavedChangesBlocker } from '../hooks/useUnsavedChangesBlocker';
import {
  Box,
  Typography,
  Paper,
  Tabs,
  Tab,
  TextField,
  Button,
  Grid,
  Switch,
  FormControlLabel,
  Alert,
  Divider,
  IconButton,
  MenuItem,
  InputAdornment,
  Radio,
  RadioGroup,
  FormControl,
  FormLabel,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  List,
  ListItem,
  ListItemText,
  Chip,
  CircularProgress,
  Stack,
} from '@mui/material';
import * as api from '../services/api';
import { ServerFileBrowser } from '../components/common/ServerFileBrowser';
import { SettingsGeneral } from '../components/SettingsGeneral';
import BlockedHashesTab from '../components/settings/BlockedHashesTab';
import { TempLoginTab } from '../components/settings/TempLoginTab';
import PluginsTab from '../components/settings/PluginsTab';
import { PathsSettingsTab } from '../components/settings/PathsSettingsTab';
import { MetadataSettingsTab } from '../components/settings/MetadataSettingsTab';
import { ToolsSettingsTab } from '../components/settings/ToolsSettingsTab';
import { ITunesImport } from '../components/settings/ITunesImport';
import { ITunesTransfer } from '../components/settings/ITunesTransfer';
import { SystemInfoTab } from '../components/system/SystemInfoTab';
import {
  Save as SaveIcon,
  RestartAlt as RestartAltIcon,
  CheckBox as CheckBoxIcon,
  CheckBoxOutlineBlank as CheckBoxOutlineBlankIcon,
  FolderOpen as FolderOpenIcon,
  Add as AddIcon,
  Delete as DeleteIcon,
  ContentCopy as ContentCopyIcon,
  VpnKey as VpnKeyIcon,
} from '@mui/icons-material';
import {
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Tooltip,
  Checkbox,
  FormGroup,
  Select,
  InputLabel,
  FormControl as MuiFormControl,
} from '@mui/material';

interface TabPanelProps {
  children?: React.ReactNode;
  index: number;
  value: number;
}

interface ScanStatus {
  status: 'scanning' | 'complete' | 'error' | 'cancelled';
  scanned: number;
  total: number;
  operationId?: string;
  errors?: string[];
}

interface ScanErrorTarget {
  path: string;
  errors: string[];
}

function TabPanel(props: TabPanelProps) {
  const { children, value, index, ...other } = props;

  return (
    <Box
      role="tabpanel"
      hidden={value !== index}
      id={`settings-tabpanel-${index}`}
      aria-labelledby={`settings-tab-${index}`}
      sx={{
        overflowY: 'auto',
        overflowX: 'hidden',
        flex: 1,
        minHeight: 0,
        p: 3,
      }}
      {...other}
    >
      {value === index && children}
    </Box>
  );
}

// ── API Keys Tab ──────────────────────────────────────────────────────────────

const ALL_SCOPES = [
  'library.view',
  'library.edit_metadata',
  'library.delete',
  'library.organize',
  'scan.trigger',
  'integrations.manage',
  'users.manage',
  'settings.manage',
  'playlists.create',
  'requests.create',
  'requests.approve',
];

const EXPIRES_OPTIONS = [
  { label: '30 days', value: 30 },
  { label: '60 days', value: 60 },
  { label: '90 days', value: 90 },
  { label: '180 days', value: 180 },
  { label: '365 days', value: 365 },
  { label: 'Never', value: 0 },
];

function relativeTime(dateStr: string): string {
  const date = new Date(dateStr);
  const days = Math.floor((Date.now() - date.getTime()) / 86400000);
  if (days === 0) return 'Today';
  if (days === 1) return 'Yesterday';
  if (days < 30) return `${days}d ago`;
  if (days < 365) return `${Math.floor(days / 30)}mo ago`;
  return `${Math.floor(days / 365)}y ago`;
}

function statusChip(status: string) {
  const colors: Record<string, 'success' | 'warning' | 'error' | 'default'> = {
    active: 'success',
    inactive: 'warning',
    revoked: 'error',
  };
  return (
    <Chip
      label={status}
      color={colors[status] ?? 'default'}
      size="small"
      sx={{ textTransform: 'capitalize' }}
    />
  );
}

function APIKeysTab() {
  const [keys, setKeys] = useState<api.APIKey[]>([]);
  const [loading, setLoading] = useState(true);
  const [showAll, setShowAll] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [createOpen, setCreateOpen] = useState(false);
  const [newName, setNewName] = useState('');
  const [newDesc, setNewDesc] = useState('');
  const [newScopes, setNewScopes] = useState<string[]>([]);
  const [newExpires, setNewExpires] = useState(90);
  const [creating, setCreating] = useState(false);
  const [createdToken, setCreatedToken] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const isUnmountedRef = useRef(false);

  const fetchKeys = async (all: boolean) => {
    setLoading(true);
    setError(null);
    try {
      const data = await api.listAPIKeys(all);
      setKeys(data);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load API keys');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchKeys(showAll);
  }, [showAll]);

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

  const handleCreate = async () => {
    if (!newName.trim()) return;
    setCreating(true);
    try {
      const resp = await api.createAPIKey({
        name: newName,
        description: newDesc,
        scopes: newScopes,
        expires_in_days: newExpires,
      });
      setCreatedToken(resp.token);
      setCreateOpen(false);
      setNewName('');
      setNewDesc('');
      setNewScopes([]);
      setNewExpires(90);
      fetchKeys(showAll);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to create API key');
    } finally {
      setCreating(false);
    }
  };

  const handleToggleStatus = async (key: api.APIKey) => {
    const next: 'active' | 'inactive' = key.status === 'active' ? 'inactive' : 'active';
    try {
      await api.updateAPIKeyStatus(key.id, next);
      fetchKeys(showAll);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to update status');
    }
  };

  const handleRevoke = async (key: api.APIKey) => {
    if (!window.confirm(`Permanently revoke "${key.name}"? This cannot be undone.`)) return;
    try {
      await api.revokeAPIKey(key.id);
      fetchKeys(showAll);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to revoke key');
    }
  };

  const copyToken = () => {
    if (!createdToken) return;
    navigator.clipboard.writeText(createdToken).then(() => {
      setCopied(true);
      // Clear any existing timeout
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
      }
      // Schedule reset with cleanup protection
      timeoutRef.current = setTimeout(() => {
        if (!isUnmountedRef.current) {
          setCopied(false);
        }
        timeoutRef.current = null;
      }, 2000);
    });
  };

  return (
    <Box>
      <Stack direction="row" alignItems="center" justifyContent="space-between" mb={2}>
        <Typography variant="h6">
          <VpnKeyIcon sx={{ mr: 1, verticalAlign: 'middle' }} />
          API Keys
        </Typography>
        <Stack direction="row" spacing={1} alignItems="center">
          <FormControlLabel
            control={
              <Switch
                checked={showAll}
                onChange={(e) => setShowAll(e.target.checked)}
                size="small"
              />
            }
            label="All Keys"
          />
          <Button variant="contained" startIcon={<AddIcon />} onClick={() => setCreateOpen(true)}>
            Create API Key
          </Button>
        </Stack>
      </Stack>

      {error && <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>{error}</Alert>}

      {loading ? (
        <CircularProgress />
      ) : (
        <TableContainer component={Paper} variant="outlined">
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Name</TableCell>
                <TableCell>Identifier</TableCell>
                <TableCell>Scopes</TableCell>
                <TableCell>Status</TableCell>
                <TableCell>Last Used</TableCell>
                <TableCell>Expires</TableCell>
                <TableCell>Age</TableCell>
                {showAll && <TableCell>User</TableCell>}
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {keys.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={showAll ? 9 : 8} align="center">
                    <Typography color="text.secondary" variant="body2">No API keys yet</Typography>
                  </TableCell>
                </TableRow>
              ) : keys.map((k) => {
                const age = Math.floor((Date.now() - new Date(k.created_at).getTime()) / 86400000);
                const neverUsed = k.never_used;
                const staleUsage = !neverUsed && k.days_since_last_use != null && k.days_since_last_use > 365;
                const warnUsage = !neverUsed && k.days_since_last_use != null && k.days_since_last_use > 30;
                const expired = k.expires_at ? new Date(k.expires_at) < new Date() : false;
                const expiringSoon = k.expires_at && !expired
                  ? (new Date(k.expires_at).getTime() - Date.now()) < 14 * 86400000
                  : false;

                return (
                  <TableRow key={k.id} sx={{ opacity: k.status === 'revoked' ? 0.5 : 1 }}>
                    <TableCell>
                      <Tooltip title={k.description || ''} placement="top">
                        <Typography variant="body2" fontWeight={500}>{k.name}</Typography>
                      </Tooltip>
                    </TableCell>
                    <TableCell>
                      <Typography variant="caption" fontFamily="monospace" color="text.secondary">
                        {k.identifier}
                      </Typography>
                    </TableCell>
                    <TableCell>
                      <Stack direction="row" spacing={0.5} flexWrap="wrap">
                        {(k.scopes ?? []).map((s) => (
                          <Chip key={s} label={s} size="small" variant="outlined" />
                        ))}
                        {(k.scopes ?? []).length === 0 && (
                          <Typography variant="caption" color="text.secondary">none</Typography>
                        )}
                      </Stack>
                    </TableCell>
                    <TableCell>{statusChip(k.status)}</TableCell>
                    <TableCell>
                      {neverUsed ? (
                        <Chip label="Never" color="warning" size="small" />
                      ) : (
                        <Tooltip title={k.last_used_at ? new Date(k.last_used_at).toLocaleString() : ''}>
                          <Chip
                            label={k.last_used_at ? relativeTime(k.last_used_at) : '—'}
                            color={staleUsage ? 'error' : warnUsage ? 'warning' : 'default'}
                            size="small"
                          />
                        </Tooltip>
                      )}
                    </TableCell>
                    <TableCell>
                      {k.expires_at ? (
                        <Chip
                          label={expired ? 'Expired' : relativeTime(k.expires_at)}
                          color={expired || expiringSoon ? 'error' : 'default'}
                          size="small"
                        />
                      ) : (
                        <Typography variant="caption" color="text.secondary">Never</Typography>
                      )}
                    </TableCell>
                    <TableCell>
                      <Typography variant="caption">{age}d</Typography>
                    </TableCell>
                    {showAll && <TableCell><Typography variant="caption">{k.username}</Typography></TableCell>}
                    <TableCell align="right">
                      <Stack direction="row" spacing={0.5} justifyContent="flex-end">
                        {k.status !== 'revoked' && (
                          <Tooltip title={k.status === 'active' ? 'Deactivate' : 'Activate'}>
                            <IconButton
                              size="small"
                              onClick={() => handleToggleStatus(k)}
                            >
                              {k.status === 'active' ? <CheckBoxIcon fontSize="small" /> : <CheckBoxOutlineBlankIcon fontSize="small" />}
                            </IconButton>
                          </Tooltip>
                        )}
                        {k.status !== 'revoked' && (
                          <Tooltip title="Revoke permanently">
                            <IconButton size="small" color="error" onClick={() => handleRevoke(k)}>
                              <DeleteIcon fontSize="small" />
                            </IconButton>
                          </Tooltip>
                        )}
                      </Stack>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      {/* Create API Key Dialog */}
      <Dialog open={createOpen} onClose={() => setCreateOpen(false)} maxWidth="sm" fullWidth>
        <DialogTitle>Create API Key</DialogTitle>
        <DialogContent>
          <Stack spacing={2} sx={{ mt: 1 }}>
            <TextField
              label="Name"
              required
              fullWidth
              value={newName}
              onChange={(e) => setNewName(e.target.value)}
            />
            <TextField
              label="Description"
              fullWidth
              multiline
              rows={2}
              value={newDesc}
              onChange={(e) => setNewDesc(e.target.value)}
            />
            <Box>
              <Typography variant="body2" gutterBottom>Scopes</Typography>
              <FormGroup>
                {ALL_SCOPES.map((s) => (
                  <FormControlLabel
                    key={s}
                    control={
                      <Checkbox
                        checked={newScopes.includes(s)}
                        onChange={(e) => {
                          if (e.target.checked) setNewScopes((p) => [...p, s]);
                          else setNewScopes((p) => p.filter((x) => x !== s));
                        }}
                        size="small"
                      />
                    }
                    label={<Typography variant="caption" fontFamily="monospace">{s}</Typography>}
                  />
                ))}
              </FormGroup>
            </Box>
            <MuiFormControl fullWidth size="small">
              <InputLabel>Expires in</InputLabel>
              <Select
                label="Expires in"
                value={newExpires}
                onChange={(e) => setNewExpires(Number(e.target.value))}
              >
                {EXPIRES_OPTIONS.map((o) => (
                  <MenuItem key={o.value} value={o.value}>{o.label}</MenuItem>
                ))}
              </Select>
            </MuiFormControl>
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setCreateOpen(false)}>Cancel</Button>
          <Button
            variant="contained"
            onClick={handleCreate}
            disabled={!newName.trim() || creating}
          >
            {creating ? 'Creating...' : 'Create'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* One-time token display */}
      <Dialog open={!!createdToken} maxWidth="sm" fullWidth>
        <DialogTitle>API Key Created</DialogTitle>
        <DialogContent>
          <Alert severity="warning" sx={{ mb: 2 }}>
            Copy this token now. You will not be able to see it again.
          </Alert>
          <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, bgcolor: 'action.hover', p: 1.5, borderRadius: 1 }}>
            <Typography fontFamily="monospace" fontSize="0.8rem" sx={{ wordBreak: 'break-all', flex: 1 }}>
              {createdToken}
            </Typography>
            <Tooltip title={copied ? 'Copied!' : 'Copy'}>
              <IconButton onClick={copyToken} size="small">
                <ContentCopyIcon fontSize="small" />
              </IconButton>
            </Tooltip>
          </Box>
        </DialogContent>
        <DialogActions>
          <Button
            variant="contained"
            onClick={() => { setCreatedToken(null); setCopied(false); }}
          >
            I've saved this, close
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}

const TAB_KEYS = ['library', 'itunes', 'metadata', 'paths', 'performance', 'security', 'api-keys', 'plugins', 'tools', 'system'] as const;

function tabFromHash(hash: string): number {
  const key = hash.replace('#', '');
  const idx = TAB_KEYS.indexOf(key as (typeof TAB_KEYS)[number]);
  return idx >= 0 ? idx : 0;
}

interface UpdatesSectionProps {
  settings: {
    autoUpdateEnabled: boolean;
    autoUpdateChannel: string;
    autoUpdateCheckMinutes: number;
    autoUpdateWindowStart: number;
    autoUpdateWindowEnd: number;
  };
  setSettings: React.Dispatch<React.SetStateAction<SettingsState>>;
}

function UpdatesSection({ settings, setSettings }: UpdatesSectionProps) {
  const [updateInfo, setUpdateInfo] = useState<api.UpdateInfo | null>(null);
  const [checking, setChecking] = useState(false);
  const [applying, setApplying] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirmOpen, setConfirmOpen] = useState(false);

  useEffect(() => {
    api.getUpdateStatus().then(setUpdateInfo).catch((err) => console.error('Failed to get update status:', err));
  }, []);

  const handleCheck = async () => {
    setChecking(true);
    setError(null);
    try {
      const info = await api.checkForUpdate();
      setUpdateInfo(info);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Check failed');
    } finally {
      setChecking(false);
    }
  };

  const handleApply = async () => {
    setConfirmOpen(false);
    setApplying(true);
    setError(null);
    try {
      await api.applyUpdate();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Update failed');
    } finally {
      setApplying(false);
    }
  };

  const hourOptions = Array.from({ length: 24 }, (_, i) => i);

  return (
    <Paper sx={{ mt: 4, p: 3 }}>
      <Typography variant="h6" gutterBottom>
        Updates
      </Typography>

      <Grid container spacing={2}>
        <Grid item xs={12}>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
            Current version: <strong>{updateInfo?.current_version || 'loading...'}</strong>
          </Typography>
          {updateInfo?.update_available && (
            <Alert severity="info" sx={{ mb: 2 }}>
              Update available: {updateInfo.latest_version}
              {updateInfo.release_url && (
                <> &mdash; <a href={updateInfo.release_url} target="_blank" rel="noreferrer">Release notes</a></>
              )}
            </Alert>
          )}
          {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}
        </Grid>

        <Grid item xs={12} sm={6}>
          <FormControlLabel
            control={
              <Switch
                checked={settings.autoUpdateEnabled}
                onChange={(e) =>
                  setSettings((prev) => ({
                    ...prev,
                    autoUpdateEnabled: e.target.checked,
                  }))
                }
              />
            }
            label="Enable automatic updates"
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            select
            fullWidth
            label="Update channel"
            value={settings.autoUpdateChannel}
            onChange={(e) =>
              setSettings((prev) => ({
                ...prev,
                autoUpdateChannel: e.target.value,
              }))
            }
            size="small"
          >
            <MenuItem value="stable">Stable</MenuItem>
            <MenuItem value="develop">Develop</MenuItem>
          </TextField>
        </Grid>

        <Grid item xs={12} sm={4}>
          <TextField
            fullWidth
            type="number"
            label="Check interval (minutes)"
            value={settings.autoUpdateCheckMinutes}
            onChange={(e) =>
              setSettings((prev) => ({
                ...prev,
                autoUpdateCheckMinutes: parseInt(e.target.value) || 60,
              }))
            }
            size="small"
            inputProps={{ min: 1 }}
          />
        </Grid>

        <Grid item xs={12} sm={4}>
          <TextField
            select
            fullWidth
            label="Update window start"
            value={settings.autoUpdateWindowStart}
            onChange={(e) =>
              setSettings((prev) => ({
                ...prev,
                autoUpdateWindowStart: parseInt(e.target.value),
              }))
            }
            size="small"
          >
            {hourOptions.map((h) => (
              <MenuItem key={h} value={h}>
                {String(h).padStart(2, '0')}:00
              </MenuItem>
            ))}
          </TextField>
        </Grid>

        <Grid item xs={12} sm={4}>
          <TextField
            select
            fullWidth
            label="Update window end"
            value={settings.autoUpdateWindowEnd}
            onChange={(e) =>
              setSettings((prev) => ({
                ...prev,
                autoUpdateWindowEnd: parseInt(e.target.value),
              }))
            }
            size="small"
          >
            {hourOptions.map((h) => (
              <MenuItem key={h} value={h}>
                {String(h).padStart(2, '0')}:00
              </MenuItem>
            ))}
          </TextField>
        </Grid>

        <Grid item xs={12}>
          <Stack direction="row" spacing={2} alignItems="center">
            <Button
              variant="outlined"
              onClick={handleCheck}
              disabled={checking}
            >
              {checking ? <CircularProgress size={20} sx={{ mr: 1 }} /> : null}
              Check Now
            </Button>
            {updateInfo?.update_available && (
              <Button
                variant="contained"
                color="warning"
                onClick={() => setConfirmOpen(true)}
                disabled={applying}
              >
                {applying ? <CircularProgress size={20} sx={{ mr: 1 }} /> : null}
                Update Now
              </Button>
            )}
            {updateInfo?.last_checked && (
              <Typography variant="caption" color="text.secondary">
                Last checked: {new Date(updateInfo.last_checked).toLocaleString()}
              </Typography>
            )}
          </Stack>
        </Grid>
      </Grid>

      <Dialog open={confirmOpen} onClose={() => setConfirmOpen(false)}>
        <DialogTitle>Apply Update</DialogTitle>
        <DialogContent>
          <Typography>
            This will download and apply the update to version{' '}
            <strong>{updateInfo?.latest_version}</strong>, then restart the
            server. The page will be temporarily unavailable.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmOpen(false)}>Cancel</Button>
          <Button onClick={handleApply} color="warning" variant="contained">
            Update and Restart
          </Button>
        </DialogActions>
      </Dialog>
    </Paper>
  );
}

interface MaintenanceWindowSectionProps {
  settings: {
    maintenanceWindowEnabled: boolean;
    maintenanceWindowStart: number;
    maintenanceWindowEnd: number;
  };
  setSettings: React.Dispatch<React.SetStateAction<SettingsState>>;
}

function MaintenanceWindowSection({ settings, setSettings }: MaintenanceWindowSectionProps) {
  const hourOptions = Array.from({ length: 24 }, (_, i) => i);

  return (
    <Paper sx={{ mt: 4, p: 3 }}>
      <Typography variant="h6" gutterBottom>
        Maintenance Window
      </Typography>

      <Grid container spacing={2}>
        <Grid item xs={12} sm={6}>
          <FormControlLabel
            control={
              <Switch
                checked={settings.maintenanceWindowEnabled}
                onChange={(e) =>
                  setSettings((prev) => ({
                    ...prev,
                    maintenanceWindowEnabled: e.target.checked,
                  }))
                }
              />
            }
            label="Enable maintenance window"
          />
        </Grid>

        <Grid item xs={12} sm={3}>
          <TextField
            select
            fullWidth
            label="Window start (hour)"
            value={settings.maintenanceWindowStart}
            onChange={(e) =>
              setSettings((prev) => ({
                ...prev,
                maintenanceWindowStart: parseInt(e.target.value),
              }))
            }
            size="small"
          >
            {hourOptions.map((h) => (
              <MenuItem key={h} value={h}>
                {String(h).padStart(2, '0')}:00
              </MenuItem>
            ))}
          </TextField>
        </Grid>

        <Grid item xs={12} sm={3}>
          <TextField
            select
            fullWidth
            label="Window end (hour)"
            value={settings.maintenanceWindowEnd}
            onChange={(e) =>
              setSettings((prev) => ({
                ...prev,
                maintenanceWindowEnd: parseInt(e.target.value),
              }))
            }
            size="small"
          >
            {hourOptions.map((h) => (
              <MenuItem key={h} value={h}>
                {String(h).padStart(2, '0')}:00
              </MenuItem>
            ))}
          </TextField>
        </Grid>
      </Grid>
    </Paper>
  );
}

interface UiMetadataSource {
  id: string;
  name: string;
  enabled: boolean;
  priority: number;
  requiresAuth: boolean;
  credentials: { [key: string]: string };
}

interface SettingsState {
  libraryPath: string;
  organizationStrategy: string;
  scanOnStartup: boolean;
  autoOrganize: boolean;
  folderNamingPattern: string;
  fileNamingPattern: string;
  createBackups: boolean;
  supportedExtensions: string[];
  excludePatterns: string[];
  enableDiskQuota: boolean;
  diskQuotaPercent: number;
  enableUserQuotas: boolean;
  defaultUserQuotaGB: number;
  autoFetchMetadata: boolean;
  enableAIParsing: boolean;
  metadataLLMScoringEnabled: boolean;
  openaiApiKey: string;
  metadataSources: UiMetadataSource[];
  language: string;
  concurrentScans: number;
  memoryLimitType: string;
  cacheSize: number;
  cacheInvalidateOnBookUpdate: boolean;
  metadataFetchCacheTTLDays: number;
  memoryLimitPercent: number;
  memoryLimitMB: number;
  logLevel: string;
  logFormat: string;
  enableJsonLogging: boolean;
  purgeSoftDeletedAfterDays: number;
  purgeSoftDeletedDeleteFiles: boolean;
  autoUpdateEnabled: boolean;
  autoUpdateChannel: string;
  autoUpdateCheckMinutes: number;
  autoUpdateWindowStart: number;
  autoUpdateWindowEnd: number;
  maintenanceWindowEnabled: boolean;
  maintenanceWindowStart: number;
  maintenanceWindowEnd: number;
  pathFormat: string;
  segmentTitleFormat: string;
  autoRenameOnApply: boolean;
  autoWriteTagsOnApply: boolean;
  verifyAfterWrite: boolean;

  // Deluge integration
  protectedPaths: string;
}

export function Settings() {
  const navigate = useNavigate();
  const location = useLocation();
  const [tabValue, setTabValue] = useState(() => tabFromHash(location.hash));
  const [browserOpen, setBrowserOpen] = useState(false);
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const scanIntervalsRef = useRef<Record<number, number>>({});

  // Import folder management
  const [importPaths, setImportFolders] = useState<api.ImportPath[]>([]);
  const [addFolderDialogOpen, setAddFolderDialogOpen] = useState(false);
  const [newFolderPath, setNewFolderPath] = useState('');
  const [showFolderBrowser, setShowFolderBrowser] = useState(false);
  const [scanStatuses, setScanStatuses] = useState<
    Record<number, ScanStatus>
  >({});
  const [cancelScanTarget, setCancelScanTarget] =
    useState<api.ImportPath | null>(null);
  const [scanErrorTarget, setScanErrorTarget] =
    useState<ScanErrorTarget | null>(null);
  const [backups, setBackups] = useState<api.BackupInfo[]>([]);
  const [backupsLoading, setBackupsLoading] = useState(false);
  const [backupNotice, setBackupNotice] = useState<{
    severity: 'success' | 'error' | 'info';
    message: string;
  } | null>(null);
  const [restoreDialogOpen, setRestoreDialogOpen] = useState(false);
  const [restoreTarget, setRestoreTarget] = useState<api.BackupInfo | null>(
    null
  );
  const [restoreInProgress, setRestoreInProgress] = useState(false);
  const [restoreVerify, setRestoreVerify] = useState(true);
  const [deleteBackupTarget, setDeleteBackupTarget] =
    useState<api.BackupInfo | null>(null);
  const [deleteBackupInProgress, setDeleteBackupInProgress] = useState(false);
  const [createBackupInProgress, setCreateBackupInProgress] = useState(false);
  const [openaiTestState, setOpenaiTestState] = useState<{
    status: 'idle' | 'loading' | 'success' | 'error';
    message?: string;
    model?: string;
  }>({ status: 'idle' });
  const [libraryPathError, setLibraryPathError] = useState<string | null>(null);
  const [openaiKeyError, setOpenaiKeyError] = useState<string | null>(null);
  const [extensionsInput, setExtensionsInput] = useState('');
  const [excludePatternInput, setExcludePatternInput] = useState('');
  const [extensionsError, setExtensionsError] = useState<string | null>(null);
  const [excludePatternError, setExcludePatternError] =
    useState<string | null>(null);
  const [importDialogOpen, setImportDialogOpen] = useState(false);
  const [importPayload, setImportPayload] =
    useState<Partial<api.Config> | null>(null);
  const [importFileName, setImportFileName] = useState('');
  const [importNotice, setImportNotice] = useState<string | null>(null);
  const [exportInProgress, setExportInProgress] = useState(false);
  const [importInProgress, setImportInProgress] = useState(false);
  const [savedSnapshot, setSavedSnapshot] = useState('');
  const [configLoaded, setConfigLoaded] = useState(false);
  const importInputRef = useRef<HTMLInputElement | null>(null);
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const isUnmountedRef = useRef(false);

  // Factory reset state
  const [factoryResetStep, setFactoryResetStep] = useState<0 | 1 | 2>(0);
  const [factoryResetConfirmText, setFactoryResetConfirmText] = useState('');
  const [factoryResetInProgress, setFactoryResetInProgress] = useState(false);

  const initialSettings: SettingsState = {
    // Library settings
    libraryPath: '/path/to/audiobooks/library',
    // 'auto', 'copy', 'hardlink', 'reflink', 'symlink'
    organizationStrategy: 'auto',
    scanOnStartup: false,
    autoOrganize: true,
    folderNamingPattern: '{author}/{series}/{title} ({print_year})',
    fileNamingPattern: '{title} - {author} - read by {narrator}',
    createBackups: true,
    supportedExtensions: ['.m4b', '.mp3', '.m4a'],
    excludePatterns: [],

    // Storage quotas
    enableDiskQuota: false,
    diskQuotaPercent: 80,
    enableUserQuotas: false,
    defaultUserQuotaGB: 100,

    // Metadata settings
    autoFetchMetadata: true,
    enableAIParsing: false,
    metadataLLMScoringEnabled: false,
    openaiApiKey: '',
    metadataSources: [
      {
        id: 'audible',
        name: 'Audible',
        enabled: true,
        priority: 1,
        requiresAuth: false,
        credentials: {},
      },
      {
        id: 'openlibrary',
        name: 'Open Library',
        enabled: true,
        priority: 2,
        requiresAuth: false,
        credentials: {},
      },
      {
        id: 'audnexus',
        name: 'Audnexus',
        enabled: true,
        priority: 3,
        requiresAuth: false,
        credentials: {},
      },
      {
        id: 'google-books',
        name: 'Google Books',
        enabled: false,
        priority: 4,
        requiresAuth: true,
        credentials: { apiKey: '' },
      },
      {
        id: 'hardcover',
        name: 'Hardcover',
        enabled: false,
        priority: 5,
        requiresAuth: true,
        credentials: { apiKey: '' },
      },
    ],
    language: 'en',

    // Performance settings
    concurrentScans: 4,

    // Memory management
    // 'items', 'percent', 'absolute'
    memoryLimitType: 'items',
    cacheSize: 1000, // items
    cacheInvalidateOnBookUpdate: false,
    metadataFetchCacheTTLDays: 30,
    memoryLimitPercent: 25, // % of system memory
    memoryLimitMB: 512, // MB

    // Lifecycle / retention
    purgeSoftDeletedAfterDays: 30,
    purgeSoftDeletedDeleteFiles: false,

    // Logging
    logLevel: 'info',
    logFormat: 'text', // 'text' or 'json'
    enableJsonLogging: false,

    // Auto-update
    autoUpdateEnabled: false,
    autoUpdateChannel: 'stable',
    autoUpdateCheckMinutes: 60,
    autoUpdateWindowStart: 1,
    autoUpdateWindowEnd: 4,

    // Maintenance window
    maintenanceWindowEnabled: false,
    maintenanceWindowStart: 2,
    maintenanceWindowEnd: 4,

    // Smart apply pipeline
    pathFormat: '{author}/{series_prefix}{title}/{track_title}.{ext}',
    segmentTitleFormat: '{title} - {track}/{total_tracks}',
    autoRenameOnApply: true,
    autoWriteTagsOnApply: true,
    verifyAfterWrite: true,

    // Deluge integration
    protectedPaths: '',
  };

  const [settings, setSettings] = useState<SettingsState>(initialSettings);
  const [saved, setSaved] = useState(false);
  const [expandedSource, setExpandedSource] = useState<string | null>(null);
  const [sourceTestStatus, setSourceTestStatus] = useState<Record<string, { testing: boolean; result?: { success: boolean; message?: string; error?: string } }>>({});
  const [savedApiKeyMask, setSavedApiKeyMask] = useState<string>('');
  const settingsSnapshot = useMemo(
    () => JSON.stringify(settings),
    [settings]
  );
  const hasUnsavedChanges =
    savedSnapshot !== '' && settingsSnapshot !== savedSnapshot;
  const blocker = useUnsavedChangesBlocker(hasUnsavedChanges);
  const savedSettings = useMemo(() => {
    if (!savedSnapshot) return null;
    try {
      return JSON.parse(savedSnapshot) as SettingsState;
    } catch {
      return null;
    }
  }, [savedSnapshot]);
  const importKeys = useMemo(() => {
    if (!importPayload) return [];
    return Object.keys(importPayload);
  }, [importPayload]);

  // Load configuration on mount
  useEffect(() => {
    loadConfig();
    loadImportFolders();
    loadBackups();
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!hasUnsavedChanges) return;
    const handleBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', handleBeforeUnload);
    return () => window.removeEventListener('beforeunload', handleBeforeUnload);
  }, [hasUnsavedChanges]);

  // Cleanup save timeout + scan intervals on unmount
  useEffect(() => {
    return () => {
      isUnmountedRef.current = true;
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
        timeoutRef.current = null;
      }
      // Clear all active scan intervals
      Object.values(scanIntervalsRef.current).forEach((interval) => {
        window.clearInterval(interval);
      });
      scanIntervalsRef.current = {};
    };
  }, []);

  const loadConfig = async () => {
    try {
      const config = await api.getConfig();
      // Store masked key if present
      if (config.openai_api_key && config.openai_api_key.includes('***')) {
        setSavedApiKeyMask(config.openai_api_key);
      }
      // Map all backend config fields to frontend settings format
      const nextSettings: SettingsState = {
        // Library settings
        libraryPath: config.root_dir || '',
        organizationStrategy: config.organization_strategy || 'auto',
        scanOnStartup: config.scan_on_startup ?? false,
        autoOrganize: config.auto_organize ?? true,
        folderNamingPattern:
          config.folder_naming_pattern ||
          '{author}/{series}/{title} ({print_year})',
        fileNamingPattern:
          config.file_naming_pattern ||
          '{title} - {author} - read by {narrator}',
        createBackups: config.create_backups ?? true,
        supportedExtensions: config.supported_extensions?.length
          ? config.supported_extensions
          : ['.m4b', '.mp3', '.m4a'],
        excludePatterns: config.exclude_patterns || [],

        // Storage quotas
        enableDiskQuota: config.enable_disk_quota ?? false,
        diskQuotaPercent: config.disk_quota_percent || 80,
        enableUserQuotas: config.enable_user_quotas ?? false,
        defaultUserQuotaGB: config.default_user_quota_gb || 100,

        // Metadata settings
        autoFetchMetadata: config.auto_fetch_metadata ?? true,
        enableAIParsing: config.enable_ai_parsing ?? false,
        metadataLLMScoringEnabled: config.metadata_llm_scoring_enabled ?? false,
        openaiApiKey: '', // Clear field when loading, show placeholder instead
        metadataSources:
          config.metadata_sources && config.metadata_sources.length > 0
            ? config.metadata_sources.map((source) => {
                // Force requiresAuth for sources that need API keys,
                // even if the saved config has requires_auth: false
                const authSources = ['google-books', 'hardcover'];
                return {
                  id: source.id,
                  name: source.name,
                  enabled: source.enabled,
                  priority: source.priority,
                  requiresAuth: source.requires_auth || authSources.includes(source.id),
                  credentials: authSources.includes(source.id)
                    ? { apiKey: '', ...source.credentials }
                    : source.credentials || ({} as { [key: string]: string }),
                };
              })
            : [
                {
                  id: 'audible',
                  name: 'Audible',
                  enabled: true,
                  priority: 1,
                  requiresAuth: false,
                  credentials: {},
                },
                {
                  id: 'openlibrary',
                  name: 'Open Library',
                  enabled: true,
                  priority: 2,
                  requiresAuth: false,
                  credentials: {},
                },
                {
                  id: 'audnexus',
                  name: 'Audnexus',
                  enabled: true,
                  priority: 3,
                  requiresAuth: false,
                  credentials: {},
                },
                {
                  id: 'google-books',
                  name: 'Google Books',
                  enabled: false,
                  priority: 4,
                  requiresAuth: true,
                  credentials: { apiKey: '' },
                },
                {
                  id: 'hardcover',
                  name: 'Hardcover',
                  enabled: false,
                  priority: 5,
                  requiresAuth: true,
                  credentials: { apiKey: '' },
                },
              ],
        language: config.language || 'en',

        // Performance settings
        concurrentScans: config.concurrent_scans || 4,

        // Memory management
        memoryLimitType: config.memory_limit_type || 'items',
        cacheSize: config.cache_size || 1000,
        cacheInvalidateOnBookUpdate: config.cache_invalidate_on_book_update ?? false,
        metadataFetchCacheTTLDays: config.metadata_fetch_cache_ttl_days ?? 30,
        memoryLimitPercent: config.memory_limit_percent || 25,
        memoryLimitMB: config.memory_limit_mb || 512,

        // Lifecycle / retention
        purgeSoftDeletedAfterDays: config.purge_soft_deleted_after_days ?? 30,
        purgeSoftDeletedDeleteFiles:
          config.purge_soft_deleted_delete_files ?? false,

        // Logging
        logLevel: config.log_level || 'info',
        logFormat: config.log_format || 'text',
        enableJsonLogging: config.enable_json_logging ?? false,

        // Auto-update (nested key preferred, flat fallback for compat)
        autoUpdateEnabled: config.auto_update?.enabled ?? config.auto_update_enabled ?? false,
        autoUpdateChannel: config.auto_update?.channel ?? config.auto_update_channel ?? 'stable',
        autoUpdateCheckMinutes: config.auto_update?.check_minutes ?? config.auto_update_check_minutes ?? 60,
        autoUpdateWindowStart: config.auto_update?.window_start ?? config.auto_update_window_start ?? 1,
        autoUpdateWindowEnd: config.auto_update?.window_end ?? config.auto_update_window_end ?? 4,

        // Maintenance window (nested key preferred, flat fallback for compat)
        maintenanceWindowEnabled: config.maintenance?.enabled ?? config.maintenance_window_enabled ?? false,
        maintenanceWindowStart: config.maintenance?.window_start ?? config.maintenance_window_start ?? 2,
        maintenanceWindowEnd: config.maintenance?.window_end ?? config.maintenance_window_end ?? 4,

        // Smart apply pipeline
        pathFormat: config.path_format || '{author}/{series_prefix}{title}/{track_title}.{ext}',
        segmentTitleFormat: config.segment_title_format || '{title} - {track}/{total_tracks}',
        autoRenameOnApply: config.auto_rename_on_apply ?? true,
        autoWriteTagsOnApply: config.auto_write_tags_on_apply ?? true,
        verifyAfterWrite: config.verify_after_write ?? true,

        // Deluge integration
        protectedPaths: (config.protected_paths || []).join('\n'),
      };
      setSettings(nextSettings);
      setSavedSnapshot(JSON.stringify(nextSettings));
      setConfigLoaded(true);
    } catch (error) {
      if (error instanceof api.ApiError && error.status === 401) {
        navigate('/login');
        return;
      }
      console.error('Failed to load config:', error);
    }
  };

  // Example data for "To Kill a Mockingbird" audiobook (no series)
  const normalizeExtension = (value: string): string => {
    const trimmed = value.trim();
    if (!trimmed) return '';
    const withDot = trimmed.startsWith('.') ? trimmed : `.${trimmed}`;
    return withDot.toLowerCase();
  };

  const isValidOpenAIKey = (value: string): boolean => {
    if (!value) return true;
    return /^sk-[A-Za-z0-9_-]{8,}$/.test(value);
  };

  const handleChange = (
    field: string,
    value: string | boolean | number | string[]
  ) => {
    setSettings((prev) => ({ ...prev, [field]: value }));
    setSaved(false);
    if (field === 'libraryPath') {
      setLibraryPathError(null);
    }
    if (field === 'openaiApiKey') {
      setOpenaiKeyError(null);
      setOpenaiTestState({ status: 'idle' });
    }
  };

  const handleBrowseLibraryPath = () => {
    setSelectedPath(settings.libraryPath);
    setBrowserOpen(true);
  };

  const handleBrowserSelect = (path: string, isDir: boolean) => {
    if (isDir) {
      setSelectedPath(path);
    }
  };

  const handleBrowserConfirm = () => {
    if (selectedPath) {
      handleChange('libraryPath', selectedPath);
    }
    setBrowserOpen(false);
  };

  const handleBrowserCancel = () => {
    setBrowserOpen(false);
    setSelectedPath(null);
  };

  // Import folder management handlers
  const loadImportFolders = async () => {
    try {
      const folders = await api.getImportPaths();
      setImportFolders(folders);
    } catch (error) {
      console.error('Failed to load import folders:', error);
    }
  };

  const handleAddImportFolder = async () => {
    if (!newFolderPath.trim()) return;

    try {
      const folder = await api.addImportPath(
        newFolderPath,
        newFolderPath.split('/').pop() || 'Import Folder'
      );
      setImportFolders((prev) => [...prev, folder]);
      setNewFolderPath('');
      setShowFolderBrowser(false);
      setAddFolderDialogOpen(false);
    } catch (error) {
      console.error('Failed to add import folder:', error);
    }
  };

  const handleRemoveImportFolder = async (id: number) => {
    try {
      await api.removeImportPath(id);
      setImportFolders((prev) => prev.filter((f) => f.id !== id));
    } catch (error) {
      console.error('Failed to remove import folder:', error);
    }
  };

  const handleScanImportFolder = async (folder: api.ImportPath) => {
    setScanStatuses((prev) => ({
      ...prev,
      [folder.id]: {
        status: 'scanning',
        scanned: 0,
        total: prev[folder.id]?.total || 0,
      },
    }));

    let total = 50;
    let errors: string[] = [];
    let operationId: string | undefined;

    try {
      const response = await api.startScan(folder.path);
      if (typeof response.total === 'number') {
        total = response.total;
      }
      if (Array.isArray(response.errors)) {
        errors = response.errors;
      }
      operationId = response.id;
    } catch (error) {
      console.error('Failed to scan import folder:', error);
      const message =
        error instanceof Error ? error.message : 'Scan failed.';
      setScanStatuses((prev) => ({
        ...prev,
        [folder.id]: {
          status: 'error',
          scanned: 0,
          total: 0,
          errors: [message],
        },
      }));
      return;
    }

    setScanStatuses((prev) => ({
      ...prev,
      [folder.id]: {
        status: 'scanning',
        scanned: 0,
        total,
        operationId,
        errors,
      },
    }));

    let scanned = 0;
    const increment = Math.max(1, Math.ceil(total / 10));
    // Clear any existing interval for this folder
    if (scanIntervalsRef.current[folder.id]) {
      window.clearInterval(scanIntervalsRef.current[folder.id]);
    }
    const interval = window.setInterval(() => {
      scanned += increment;
      setScanStatuses((prev) => ({
        ...prev,
        [folder.id]: {
          status: scanned >= total ? 'complete' : 'scanning',
          scanned: Math.min(scanned, total),
          total,
          operationId,
          errors,
        },
      }));
      if (scanned >= total) {
        window.clearInterval(interval);
        delete scanIntervalsRef.current[folder.id];
      }
    }, 300);

    scanIntervalsRef.current[folder.id] = interval;
  };

  const handleRequestCancelScan = (folder: api.ImportPath) => {
    setCancelScanTarget(folder);
  };

  const handleConfirmCancelScan = async () => {
    if (!cancelScanTarget) return;
    const target = cancelScanTarget;
    setCancelScanTarget(null);
    const status = scanStatuses[target.id];
    if (!status) return;
    const interval = scanIntervalsRef.current[target.id];
    if (interval) {
      window.clearInterval(interval);
      delete scanIntervalsRef.current[target.id];
    }
    if (status.operationId) {
      try {
        await api.cancelOperation(status.operationId);
      } catch (error) {
        console.error('Failed to cancel scan operation:', error);
      }
    }
    setScanStatuses((prev) => ({
      ...prev,
      [target.id]: {
        ...status,
        status: 'cancelled',
      },
    }));
  };

  const handleViewScanErrors = (
    folder: api.ImportPath,
    status: ScanStatus
  ) => {
    if (!status.errors?.length) return;
    setScanErrorTarget({
      path: folder.path,
      errors: status.errors,
    });
  };

  const loadBackups = async () => {
    setBackupsLoading(true);
    try {
      const data = await api.listBackups();
      const sorted = [...(data.backups || [])].sort(
        (a, b) =>
          new Date(b.created_at).getTime() - new Date(a.created_at).getTime()
      );
      setBackups(sorted);
    } catch (error) {
      console.error('Failed to load backups:', error);
      setBackupNotice({
        severity: 'error',
        message: 'Failed to load backups.',
      });
    } finally {
      setBackupsLoading(false);
    }
  };

  const handleCreateBackup = async () => {
    setCreateBackupInProgress(true);
    setBackupNotice(null);
    try {
      await api.createBackup();
      setBackupNotice({
        severity: 'success',
        message: 'Backup created successfully.',
      });
      await loadBackups();
    } catch (error) {
      console.error('Failed to create backup:', error);
      setBackupNotice({
        severity: 'error',
        message: 'Failed to create backup.',
      });
    } finally {
      setCreateBackupInProgress(false);
    }
  };

  const handleRequestRestore = (backup: api.BackupInfo) => {
    setRestoreTarget(backup);
    setRestoreDialogOpen(true);
  };

  const handleConfirmRestore = async () => {
    if (!restoreTarget) return;
    setRestoreInProgress(true);
    setBackupNotice(null);
    try {
      await api.restoreBackup(restoreTarget.filename, restoreVerify);
      setBackupNotice({
        severity: 'success',
        message: 'Backup restored successfully.',
      });
      setRestoreDialogOpen(false);
      window.location.reload();
    } catch (error) {
      console.error('Failed to restore backup:', error);
      setBackupNotice({
        severity: 'error',
        message: 'Backup file is corrupt.',
      });
    } finally {
      setRestoreInProgress(false);
    }
  };

  const handleRequestDeleteBackup = (backup: api.BackupInfo) => {
    setDeleteBackupTarget(backup);
  };

  const handleConfirmDeleteBackup = async () => {
    if (!deleteBackupTarget) return;
    setDeleteBackupInProgress(true);
    setBackupNotice(null);
    try {
      await api.deleteBackup(deleteBackupTarget.filename);
      setBackupNotice({
        severity: 'success',
        message: 'Backup deleted successfully.',
      });
      setDeleteBackupTarget(null);
      await loadBackups();
    } catch (error) {
      console.error('Failed to delete backup:', error);
      setBackupNotice({
        severity: 'error',
        message: 'Failed to delete backup.',
      });
    } finally {
      setDeleteBackupInProgress(false);
    }
  };

  const handleFolderBrowserSelect = (path: string, isDir: boolean) => {
    if (isDir) {
      setNewFolderPath(path);
    }
  };

  const handleSourceToggle = (sourceId: string) => {
    setSettings((prev) => ({
      ...prev,
      metadataSources: prev.metadataSources.map((source) =>
        source.id === sourceId
          ? { ...source, enabled: !source.enabled }
          : source
      ),
    }));
    setSaved(false);
  };

  const handleTestMetadataSource = async (sourceId: string) => {
    const source = settings.metadataSources.find((s) => s.id === sourceId);
    const apiKey = source?.credentials?.apiKey || '';
    if (!apiKey) {
      setSourceTestStatus((prev) => ({
        ...prev,
        [sourceId]: { testing: false, result: { success: false, error: 'No API key entered' } },
      }));
      return;
    }
    setSourceTestStatus((prev) => ({
      ...prev,
      [sourceId]: { testing: true },
    }));
    try {
      const result = await api.testMetadataSource(sourceId, apiKey);
      setSourceTestStatus((prev) => ({
        ...prev,
        [sourceId]: { testing: false, result },
      }));
    } catch (err) {
      setSourceTestStatus((prev) => ({
        ...prev,
        [sourceId]: { testing: false, result: { success: false, error: String(err) } },
      }));
    }
  };

  const handleCredentialChange = (
    sourceId: string,
    field: string,
    value: string
  ) => {
    setSettings((prev) => ({
      ...prev,
      metadataSources: prev.metadataSources.map((source) =>
        source.id === sourceId
          ? {
              ...source,
              credentials: { ...source.credentials, [field]: value },
            }
          : source
      ),
    }));
    setSaved(false);
  };

  const handleSourceReorder = (sourceId: string, direction: 'up' | 'down') => {
    setSettings((prev) => {
      const sources = [...prev.metadataSources];
      const index = sources.findIndex((s) => s.id === sourceId);
      if (index === -1) return prev;

      const targetIndex = direction === 'up' ? index - 1 : index + 1;
      if (targetIndex < 0 || targetIndex >= sources.length) return prev;

      // Swap priorities
      const temp = sources[index].priority;
      sources[index] = {
        ...sources[index],
        priority: sources[targetIndex].priority,
      };
      sources[targetIndex] = { ...sources[targetIndex], priority: temp };

      // Sort by priority
      sources.sort((a, b) => a.priority - b.priority);

      return { ...prev, metadataSources: sources };
    });
    setSaved(false);
  };

  const handleSave = async (): Promise<boolean> => {
    if (!configLoaded) {
      console.warn('[Settings] Save blocked — config not yet loaded');
      return false;
    }
    setLibraryPathError(null);
    setOpenaiKeyError(null);
    setExtensionsError(null);
    setExcludePatternError(null);

    const libraryPath = settings.libraryPath.trim();
    if (!libraryPath) {
      setLibraryPathError('Library path is required.');
      return false;
    }
    if (savedSettings && savedSettings.libraryPath !== libraryPath) {
      try {
        await api.browseFilesystem(libraryPath);
      } catch (error) {
        console.error('Library path validation failed:', error);
        setLibraryPathError('Directory does not exist.');
        return false;
      }
    }
    if (settings.supportedExtensions.length === 0) {
      setExtensionsError('Add at least one extension.');
      return false;
    }
    if (!isValidOpenAIKey(settings.openaiApiKey)) {
      setOpenaiKeyError('Invalid API key format.');
      return false;
    }

    try {
      // Map all frontend settings to backend config format
      const updates: Partial<api.Config> = {
        // Core paths
        root_dir: libraryPath,
        playlist_dir: `${libraryPath}/playlists`,

        // Library organization
        organization_strategy: settings.organizationStrategy,
        scan_on_startup: settings.scanOnStartup,
        auto_organize: settings.autoOrganize,
        folder_naming_pattern: settings.folderNamingPattern,
        file_naming_pattern: settings.fileNamingPattern,
        create_backups: settings.createBackups,
        supported_extensions: settings.supportedExtensions,
        exclude_patterns: settings.excludePatterns,

        // Storage quotas
        enable_disk_quota: settings.enableDiskQuota,
        disk_quota_percent: settings.diskQuotaPercent,
        enable_user_quotas: settings.enableUserQuotas,
        default_user_quota_gb: settings.defaultUserQuotaGB,

        // Metadata
        auto_fetch_metadata: settings.autoFetchMetadata,
        enable_ai_parsing: settings.enableAIParsing,
        metadata_llm_scoring_enabled: settings.metadataLLMScoringEnabled,
        // Only include API key if user entered a new one
        ...(settings.openaiApiKey
          ? { openai_api_key: settings.openaiApiKey }
          : {}),
        metadata_sources: settings.metadataSources.map((source) => ({
          id: source.id,
          name: source.name,
          enabled: source.enabled,
          priority: source.priority,
          requires_auth: source.requiresAuth,
          credentials: source.credentials as { [key: string]: string },
        })),
        language: settings.language,

        // Performance
        concurrent_scans: settings.concurrentScans,

        // Memory management
        memory_limit_type: settings.memoryLimitType,
        cache_size: settings.cacheSize,
        cache_invalidate_on_book_update: settings.cacheInvalidateOnBookUpdate,
        metadata_fetch_cache_ttl_days: settings.metadataFetchCacheTTLDays,
        memory_limit_percent: settings.memoryLimitPercent,
        memory_limit_mb: settings.memoryLimitMB,

        // Lifecycle / retention
        purge_soft_deleted_after_days: settings.purgeSoftDeletedAfterDays,
        purge_soft_deleted_delete_files: settings.purgeSoftDeletedDeleteFiles,

        // Logging
        log_level: settings.logLevel,
        log_format: settings.logFormat,
        enable_json_logging: settings.enableJsonLogging,

        // Auto-update — flat keys for compat + nested preferred
        auto_update_enabled: settings.autoUpdateEnabled,
        auto_update_channel: settings.autoUpdateChannel,
        auto_update_check_minutes: settings.autoUpdateCheckMinutes,
        auto_update_window_start: settings.autoUpdateWindowStart,
        auto_update_window_end: settings.autoUpdateWindowEnd,
        auto_update: {
          enabled: settings.autoUpdateEnabled,
          channel: settings.autoUpdateChannel,
          check_minutes: settings.autoUpdateCheckMinutes,
          window_start: settings.autoUpdateWindowStart,
          window_end: settings.autoUpdateWindowEnd,
        },

        // Maintenance window — flat keys for compat + nested preferred
        maintenance_window_enabled: settings.maintenanceWindowEnabled,
        maintenance_window_start: settings.maintenanceWindowStart,
        maintenance_window_end: settings.maintenanceWindowEnd,
        maintenance: {
          enabled: settings.maintenanceWindowEnabled,
          window_start: settings.maintenanceWindowStart,
          window_end: settings.maintenanceWindowEnd,
        } as api.MaintenanceConfig,

        // Smart apply pipeline
        path_format: settings.pathFormat,
        segment_title_format: settings.segmentTitleFormat,
        auto_rename_on_apply: settings.autoRenameOnApply,
        auto_write_tags_on_apply: settings.autoWriteTagsOnApply,
        verify_after_write: settings.verifyAfterWrite,
        protected_paths: settings.protectedPaths
          .split('\n')
          .map((p) => p.trim())
          .filter(Boolean),
      };

      const response = await api.updateConfig(updates);

      let nextSettings = settings;
      if (settings.openaiApiKey && response.openai_api_key) {
        setSavedApiKeyMask(response.openai_api_key);
        nextSettings = { ...settings, openaiApiKey: '' };
        setSettings(nextSettings);
      }

      setSavedSnapshot(JSON.stringify(nextSettings));
      setSaved(true);
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
      }
      timeoutRef.current = setTimeout(() => {
        if (!isUnmountedRef.current) {
          setSaved(false);
        }
        timeoutRef.current = null;
      }, 3000);
      return true;
    } catch (error) {
      if (error instanceof api.ApiError && error.status === 401) {
        navigate('/login');
        return false;
      }
      console.error('Failed to save settings:', error);
      alert('Failed to save settings. Please try again.');
      return false;
    }
  };

  const handleReset = () => {
    if (!confirm('Reset all settings to defaults?')) return;

    setSettings(initialSettings);
    setSaved(false);
    setLibraryPathError(null);
    setOpenaiKeyError(null);
    setExtensionsError(null);
    setExcludePatternError(null);
  };

  const handleAddExtension = () => {
    const normalized = normalizeExtension(extensionsInput);
    if (!normalized) {
      setExtensionsError('Enter a file extension.');
      return;
    }
    if (!/^\.[a-z0-9]+$/i.test(normalized)) {
      setExtensionsError('Use letters or numbers, like .m4b');
      return;
    }
    if (settings.supportedExtensions.includes(normalized)) {
      setExtensionsError('Extension already added.');
      return;
    }
    setSettings((prev) => ({
      ...prev,
      supportedExtensions: [...prev.supportedExtensions, normalized].sort(),
    }));
    setExtensionsInput('');
    setExtensionsError(null);
    setSaved(false);
  };

  const handleRemoveExtension = (extension: string) => {
    setSettings((prev) => ({
      ...prev,
      supportedExtensions: prev.supportedExtensions.filter(
        (item) => item !== extension
      ),
    }));
    setExtensionsError(null);
    setSaved(false);
  };

  const handleAddExcludePattern = () => {
    const normalized = excludePatternInput.trim();
    if (!normalized) {
      setExcludePatternError('Enter a pattern to exclude.');
      return;
    }
    if (settings.excludePatterns.includes(normalized)) {
      setExcludePatternError('Pattern already added.');
      return;
    }
    setSettings((prev) => ({
      ...prev,
      excludePatterns: [...prev.excludePatterns, normalized],
    }));
    setExcludePatternInput('');
    setExcludePatternError(null);
    setSaved(false);
  };

  const handleRemoveExcludePattern = (pattern: string) => {
    setSettings((prev) => ({
      ...prev,
      excludePatterns: prev.excludePatterns.filter((item) => item !== pattern),
    }));
    setExcludePatternError(null);
    setSaved(false);
  };

  const handleTestAIConnection = async () => {
    const apiKey = settings.openaiApiKey.trim();
    if (!settings.enableAIParsing) {
      setOpenaiTestState({
        status: 'error',
        message: 'Enable AI parsing to test the connection.',
      });
      return;
    }
    if (apiKey && !isValidOpenAIKey(apiKey)) {
      setOpenaiKeyError('Invalid API key format.');
      return;
    }
    if (!apiKey && !savedApiKeyMask) {
      setOpenaiTestState({
        status: 'error',
        message: 'API key not provided.',
      });
      return;
    }
    setOpenaiTestState({ status: 'loading' });
    try {
      const response = await api.testAIConnection(apiKey || undefined);
      setOpenaiTestState({
        status: 'success',
        message: response.message || 'Connection successful.',
      });
    } catch (error) {
      const message =
        error instanceof Error ? error.message : 'Connection failed.';
      setOpenaiTestState({
        status: 'error',
        message,
      });
    }
  };

  const sanitizeImportPayload = (
    payload: Partial<api.Config>
  ): Partial<api.Config> => {
    const allowed = new Set([
      'root_dir', 'playlist_dir', 'organization_strategy', 'scan_on_startup', 'auto_organize',
      'folder_naming_pattern', 'file_naming_pattern', 'create_backups', 'supported_extensions',
      'exclude_patterns', 'enable_disk_quota', 'disk_quota_percent', 'enable_user_quotas',
      'default_user_quota_gb', 'auto_fetch_metadata', 'enable_ai_parsing',
      'metadata_llm_scoring_enabled', 'openai_api_key', 'metadata_sources', 'language',
      'concurrent_scans','memory_limit_type','cache_size','cache_invalidate_on_book_update',
      'metadata_fetch_cache_ttl_days','memory_limit_percent','memory_limit_mb',
      'purge_soft_deleted_after_days','purge_soft_deleted_delete_files','log_level','log_format',
      'enable_json_logging','auto_update_enabled','auto_update_channel','auto_update_check_minutes',
      'auto_update_window_start','auto_update_window_end','maintenance_window_enabled',
      'maintenance_window_start','maintenance_window_end','path_format','segment_title_format',
      'auto_rename_on_apply','auto_write_tags_on_apply','verify_after_write','protected_paths'
    ]);

    const cleaned: Partial<api.Config> = {};
    if (!payload || typeof payload !== 'object') return cleaned;

    for (const key of Object.keys(payload)) {
      if (!allowed.has(key)) continue;
      const val = (payload as any)[key];

      switch (key) {
        case 'root_dir':
        case 'playlist_dir':
        case 'organization_strategy':
        case 'folder_naming_pattern':
        case 'file_naming_pattern':
        case 'language':
        case 'memory_limit_type':
        case 'log_level':
        case 'log_format':
        case 'auto_update_channel':
        case 'path_format':
        case 'segment_title_format':
        case 'protected_paths':
          if (typeof val === 'string') (cleaned as any)[key] = val;
          break;

        case 'supported_extensions':
        case 'exclude_patterns':
          if (Array.isArray(val)) (cleaned as any)[key] = val.filter((x) => typeof x === 'string');
          break;

        case 'metadata_sources':
          if (Array.isArray(val)) {
            const sanitizedSources = (val as any[]).map((s) => {
              if (!s || typeof s !== 'object') return null;
              const src: any = {};
              if (typeof (s as any).id === 'string') src.id = (s as any).id;
              if (typeof (s as any).name === 'string') src.name = (s as any).name;
              src.enabled = Boolean((s as any).enabled);
              src.priority = typeof (s as any).priority === 'number' ? (s as any).priority : 0;
              src.requires_auth = Boolean((s as any).requires_auth ?? (s as any).requiresAuth);
              src.credentials = {};
              if ((s as any).credentials && typeof (s as any).credentials === 'object') {
                for (const [ck, cv] of Object.entries((s as any).credentials)) {
                  if (typeof cv === 'string') src.credentials[ck] = cv;
                }
              }
              return src;
            }).filter(Boolean);
            (cleaned as any)[key] = sanitizedSources;
          }
          break;

        case 'openai_api_key':
          if (typeof val === 'string') {
            if (!val.includes('***')) (cleaned as any).openai_api_key = val;
          }
          break;

        // boolean flags
        case 'scan_on_startup':
        case 'auto_organize':
        case 'create_backups':
        case 'enable_disk_quota':
        case 'enable_user_quotas':
        case 'auto_fetch_metadata':
        case 'enable_ai_parsing':
        case 'metadata_llm_scoring_enabled':
        case 'cache_invalidate_on_book_update':
        case 'purge_soft_deleted_delete_files':
        case 'enable_json_logging':
        case 'auto_update_enabled':
        case 'maintenance_window_enabled':
        case 'auto_rename_on_apply':
        case 'auto_write_tags_on_apply':
        case 'verify_after_write':
          (cleaned as any)[key] = Boolean(val);
          break;

        // numeric fields
        case 'disk_quota_percent':
        case 'default_user_quota_gb':
        case 'concurrent_scans':
        case 'cache_size':
        case 'metadata_fetch_cache_ttl_days':
        case 'memory_limit_percent':
        case 'memory_limit_mb':
        case 'purge_soft_deleted_after_days':
        case 'auto_update_check_minutes':
        case 'auto_update_window_start':
        case 'auto_update_window_end':
        case 'maintenance_window_start':
        case 'maintenance_window_end':
          if (typeof val === 'number') (cleaned as any)[key] = val;
          else if (typeof val === 'string' && val.trim() !== '' && !isNaN(Number(val))) (cleaned as any)[key] = Number(val);
          break;

        default:
          break;
      }
    }

    return cleaned;
  };

  const handleExportSettings = async () => {
    setExportInProgress(true);
    setImportNotice(null);
    try {
      const config = await api.getConfig();
      const blob = new Blob([JSON.stringify(config, null, 2)], {
        type: 'application/json',
      });
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement('a');
      anchor.href = url;
      anchor.download = `settings-${new Date().toISOString()}.json`;
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
      URL.revokeObjectURL(url);
      setImportNotice('Settings exported.');
    } catch (error) {
      const message =
        error instanceof Error ? error.message : 'Export failed.';
      setImportNotice(message);
    } finally {
      setExportInProgress(false);
    }
  };

  const handleImportClick = () => {
    setImportNotice(null);
    if (importInputRef.current) {
      importInputRef.current.value = '';
      importInputRef.current.click();
    }
  };

  const handleImportFileChange = async (
    event: ChangeEvent<HTMLInputElement>
  ) => {
    const file = event.target.files?.[0];
    if (!file) return;
    try {
      const text = await file.text();
      const parsed = JSON.parse(text) as Partial<api.Config>;
      const cleaned = sanitizeImportPayload(parsed);
      setImportFileName(file.name);
      setImportPayload(cleaned);
      setImportDialogOpen(true);
    } catch (error) {
      const message =
        error instanceof Error ? error.message : 'Import failed.';
      setImportNotice(message);
    }
  };

  const handleConfirmImport = async () => {
    if (!importPayload) return;
    setImportInProgress(true);
    try {
      await api.updateConfig(importPayload);
      await loadConfig();
      setImportDialogOpen(false);
      setImportPayload(null);
      setImportFileName('');
      setImportNotice('Settings imported successfully.');
    } catch (error) {
      const message =
        error instanceof Error ? error.message : 'Import failed.';
      setImportNotice(message);
    } finally {
      setImportInProgress(false);
    }
  };

  const handleSaveAndNavigate = async () => {
    const success = await handleSave();
    if (success) {
      blocker.proceed?.();
    }
  };

  const handleDiscardNavigation = () => {
    blocker.proceed?.();
  };

  const handleCancelNavigation = () => {
    blocker.reset?.();
  };

  return (
    <Box
      sx={{
        display: 'flex',
        flexDirection: 'column',
        height: '100vh',
        maxHeight: '100vh',
        overflow: 'hidden',
        p: 2,
      }}
    >
      <Typography variant="h4" gutterBottom sx={{ flexShrink: 0 }}>
        Settings
      </Typography>

      {saved && (
        <Alert severity="success" sx={{ mb: 2, flexShrink: 0 }}>
          Settings saved successfully!
        </Alert>
      )}
      {importNotice && (
        <Alert severity="info" sx={{ mb: 2, flexShrink: 0 }}>
          {importNotice}
        </Alert>
      )}

      <Paper
        sx={{
          display: 'flex',
          flexDirection: 'column',
          flex: 1,
          minHeight: 0,
          overflow: 'hidden',
        }}
      >
        <Box sx={{ borderBottom: 1, borderColor: 'divider' }}>
          <Tabs
            value={tabValue}
            onChange={(_, newValue) => {
              setTabValue(newValue);
              window.history.replaceState(null, '', `#${TAB_KEYS[newValue]}`);
            }}
            aria-label="settings tabs"
            variant="scrollable"
            scrollButtons="auto"
            allowScrollButtonsMobile
          >
            <Tab label="Library" />
            <Tab label="iTunes Import" />
            <Tab label="Metadata" />
            <Tab label="Paths" />
            <Tab label="Performance" />
            <Tab label="Security" />
            <Tab label="API Keys" />
            <Tab label="Plugins" />
            <Tab label="Tools" />
            <Tab label="System Info" />
            <Tab label="Temp Login" />
          </Tabs>
        </Box>

        <TabPanel value={tabValue} index={0}>
          <SettingsGeneral
            settings={settings}
            setSettings={setSettings}
            libraryPathError={libraryPathError}
            handleChange={handleChange}
            handleBrowseLibraryPath={handleBrowseLibraryPath}
            extensionsInput={extensionsInput}
            setExtensionsInput={setExtensionsInput}
            extensionsError={extensionsError}
            handleAddExtension={handleAddExtension}
            handleRemoveExtension={handleRemoveExtension}
            excludePatternInput={excludePatternInput}
            setExcludePatternInput={setExcludePatternInput}
            excludePatternError={excludePatternError}
            handleAddExcludePattern={handleAddExcludePattern}
            handleRemoveExcludePattern={handleRemoveExcludePattern}
            backupNotice={backupNotice}
            createBackupInProgress={createBackupInProgress}
            handleCreateBackup={handleCreateBackup}
            backupsLoading={backupsLoading}
            backups={backups}
            handleRequestRestore={handleRequestRestore}
            handleRequestDeleteBackup={handleRequestDeleteBackup}
          />
        </TabPanel>

        <TabPanel value={tabValue} index={1}>
          <ITunesImport />
          <ITunesTransfer />
        </TabPanel>

        <TabPanel value={tabValue} index={2}>
          <MetadataSettingsTab
            settings={settings}
            setSettings={setSettings}
            handleChange={handleChange}
            expandedSource={expandedSource}
            setExpandedSource={setExpandedSource}
            openaiTestState={openaiTestState}
            openaiKeyError={openaiKeyError}
            savedApiKeyMask={savedApiKeyMask}
            setSavedApiKeyMask={setSavedApiKeyMask}
            sourceTestStatus={sourceTestStatus}
            handleTestAIConnection={handleTestAIConnection}
            handleSourceToggle={handleSourceToggle}
            handleTestMetadataSource={handleTestMetadataSource}
            handleCredentialChange={handleCredentialChange}
            handleSourceReorder={handleSourceReorder}
          />
        </TabPanel>

        <TabPanel value={tabValue} index={3}>
          <PathsSettingsTab
            settings={settings}
            setSettings={setSettings}
            libraryPathError={libraryPathError}
            handleChange={handleChange}
            handleBrowseLibraryPath={handleBrowseLibraryPath}
            importPaths={importPaths}
            scanStatuses={scanStatuses}
            handleViewScanErrors={handleViewScanErrors}
            handleRequestCancelScan={handleRequestCancelScan}
            handleScanImportFolder={handleScanImportFolder}
            handleRemoveImportFolder={handleRemoveImportFolder}
            setAddFolderDialogOpen={setAddFolderDialogOpen}
          />
        </TabPanel>

        <TabPanel value={tabValue} index={4}>
          <Grid container spacing={3}>
            <Grid item xs={12}>
              <Typography variant="h6" gutterBottom>
                Performance Settings
              </Typography>
              <Divider sx={{ mb: 2 }} />
            </Grid>

            <Grid item xs={12} sm={6}>
              <TextField
                fullWidth
                type="number"
                label="Concurrent Scans"
                value={settings.concurrentScans}
                onChange={(e) =>
                  handleChange('concurrentScans', parseInt(e.target.value) || 1)
                }
                inputProps={{ min: 1, max: 16 }}
                helperText="Number of folders to scan simultaneously"
              />
            </Grid>

            <Grid item xs={12}>
              <Typography variant="subtitle1" gutterBottom>
                Memory Management
              </Typography>
            </Grid>

            <Grid item xs={12}>
              <FormControl component="fieldset">
                <FormLabel component="legend">Memory Limit Type</FormLabel>
                <RadioGroup
                  row
                  value={settings.memoryLimitType}
                  onChange={(e) =>
                    handleChange('memoryLimitType', e.target.value)
                  }
                >
                  <FormControlLabel
                    value="items"
                    control={<Radio />}
                    label="Number of Items"
                  />
                  <FormControlLabel
                    value="percent"
                    control={<Radio />}
                    label="% of System Memory"
                  />
                  <FormControlLabel
                    value="absolute"
                    control={<Radio />}
                    label="Absolute MB"
                  />
                </RadioGroup>
              </FormControl>
            </Grid>

            {settings.memoryLimitType === 'items' && (
              <Grid item xs={12} sm={6}>
                <TextField
                  fullWidth
                  type="number"
                  label="Cache Size (items)"
                  value={settings.cacheSize}
                  onChange={(e) =>
                    handleChange('cacheSize', parseInt(e.target.value) || 100)
                  }
                  inputProps={{ min: 100, max: 10000 }}
                  helperText="Number of audiobook records to cache in memory"
                />
              </Grid>
            )}

            <Grid item xs={12} sm={6}>
              <TextField
                fullWidth
                type="number"
                label="Metadata fetch cache TTL (days)"
                value={settings.metadataFetchCacheTTLDays}
                onChange={(e) =>
                  handleChange('metadataFetchCacheTTLDays', parseInt(e.target.value) || 0)
                }
                inputProps={{ min: 0, max: 365 }}
                helperText="How long to keep Audible/Audnexus API results before re-fetching. 0 = never expire."
              />
            </Grid>

            <Grid item xs={12}>
              <FormControlLabel
                control={
                  <Switch
                    checked={settings.cacheInvalidateOnBookUpdate}
                    onChange={(e) =>
                      handleChange('cacheInvalidateOnBookUpdate', e.target.checked)
                    }
                  />
                }
                label="Invalidate list cache on book update"
              />
              <Typography variant="caption" color="text.secondary" display="block">
                When off (default), metadata fetches and write-back operations keep the library
                list cache warm. Turn on only if you need the library page to reflect every
                individual book update immediately.
              </Typography>
            </Grid>

            {settings.memoryLimitType === 'percent' && (
              <Grid item xs={12} sm={6}>
                <TextField
                  fullWidth
                  type="number"
                  label="Memory Limit"
                  value={settings.memoryLimitPercent}
                  onChange={(e) =>
                    handleChange(
                      'memoryLimitPercent',
                      parseInt(e.target.value) || 1
                    )
                  }
                  InputProps={{
                    endAdornment: (
                      <InputAdornment position="end">%</InputAdornment>
                    ),
                  }}
                  inputProps={{ min: 1, max: 90 }}
                  helperText="Maximum percentage of system memory to use"
                />
              </Grid>
            )}

            {settings.memoryLimitType === 'absolute' && (
              <Grid item xs={12} sm={6}>
                <TextField
                  fullWidth
                  type="number"
                  label="Memory Limit"
                  value={settings.memoryLimitMB}
                  onChange={(e) =>
                    handleChange(
                      'memoryLimitMB',
                      parseInt(e.target.value) || 128
                    )
                  }
                  InputProps={{
                    endAdornment: (
                      <InputAdornment position="end">MB</InputAdornment>
                    ),
                  }}
                  inputProps={{ min: 128, max: 16384 }}
                  helperText="Absolute memory limit in megabytes"
                />
              </Grid>
            )}

            <Grid item xs={12}>
              <Divider sx={{ my: 2 }} />
              <Typography variant="subtitle1" gutterBottom>
                Lifecycle &amp; Retention
              </Typography>
              <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
                Control how long soft-deleted books remain before automatic
                purge runs.
              </Typography>
            </Grid>

            <Grid item xs={12} sm={6}>
              <TextField
                fullWidth
                type="number"
                label="Auto-Purge After (days)"
                value={settings.purgeSoftDeletedAfterDays}
                onChange={(e) =>
                  handleChange(
                    'purgeSoftDeletedAfterDays',
                    parseInt(e.target.value) || 0
                  )
                }
                inputProps={{ min: 0, max: 365 }}
                helperText="Set to 0 to disable automatic purge"
              />
            </Grid>
            <Grid item xs={12} sm={6}>
              <FormControlLabel
                control={
                  <Switch
                    checked={settings.purgeSoftDeletedDeleteFiles}
                    onChange={(e) =>
                      handleChange(
                        'purgeSoftDeletedDeleteFiles',
                        e.target.checked
                      )
                    }
                  />
                }
                label="Delete files from disk during purge"
              />
              <Typography
                variant="caption"
                color="text.secondary"
                sx={{ display: 'block', ml: 4 }}
              >
                Disable to keep files on disk while clearing database records.
              </Typography>
            </Grid>

            <Grid item xs={12}>
              <Divider sx={{ my: 2 }} />
              <Typography variant="subtitle1" gutterBottom>
                Logging
              </Typography>
            </Grid>

            <Grid item xs={12} sm={6}>
              <TextField
                fullWidth
                select
                label="Log Level"
                value={settings.logLevel}
                onChange={(e) => handleChange('logLevel', e.target.value)}
                helperText="Logging verbosity level"
              >
                <MenuItem value="debug">Debug</MenuItem>
                <MenuItem value="info">Info</MenuItem>
                <MenuItem value="warn">Warning</MenuItem>
                <MenuItem value="error">Error</MenuItem>
              </TextField>
            </Grid>

            <Grid item xs={12} sm={6}>
              <TextField
                fullWidth
                select
                label="Log Format"
                value={settings.logFormat}
                onChange={(e) => handleChange('logFormat', e.target.value)}
              >
                <MenuItem value="text">Text (human-readable)</MenuItem>
                <MenuItem value="json">JSON (structured)</MenuItem>
              </TextField>
              <Typography
                variant="caption"
                color="text.secondary"
                sx={{ mt: 1, display: 'block' }}
              >
                JSON logging is recommended for log aggregation and analysis
                tools
              </Typography>
            </Grid>

            <Grid item xs={12}>
              <FormControlLabel
                control={
                  <Switch
                    checked={settings.enableJsonLogging}
                    onChange={(e) =>
                      handleChange('enableJsonLogging', e.target.checked)
                    }
                  />
                }
                label="Enable JSON structured logging to separate file"
              />
              <Typography
                variant="caption"
                color="text.secondary"
                sx={{ display: 'block', ml: 4 }}
              >
                Creates a separate .json log file in addition to the main log
              </Typography>
            </Grid>
          </Grid>
        </TabPanel>

        <TabPanel value={tabValue} index={5}>
          <BlockedHashesTab />
        </TabPanel>

        <TabPanel value={tabValue} index={6}>
          <APIKeysTab />
        </TabPanel>

        <TabPanel value={tabValue} index={7}>
          <PluginsTab />
        </TabPanel>

        <TabPanel value={tabValue} index={8}>
          <ToolsSettingsTab />
        </TabPanel>

        <TabPanel value={tabValue} index={9}>
          <SystemInfoTab />

          <UpdatesSection settings={settings} setSettings={setSettings} />

          <MaintenanceWindowSection settings={settings} setSettings={setSettings} />

          <Paper
            sx={{
              mt: 4,
              p: 3,
              border: 2,
              borderColor: 'error.main',
              borderRadius: 1,
            }}
          >
            <Typography variant="h6" color="error" gutterBottom>
              Danger Zone
            </Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
              Permanently delete all data including audiobooks, authors, series,
              settings, and metadata cache. This cannot be undone.
            </Typography>
            <Button
              variant="outlined"
              color="error"
              onClick={() => setFactoryResetStep(1)}
              disabled={factoryResetInProgress}
            >
              Factory Reset
            </Button>
          </Paper>

          {/* Factory Reset Dialog 1: Warning */}
          <Dialog
            open={factoryResetStep === 1}
            onClose={() => setFactoryResetStep(0)}
          >
            <DialogTitle>Factory Reset</DialogTitle>
            <DialogContent>
              <Typography>
                This will permanently delete <strong>ALL</strong> data —
                audiobooks, authors, series, settings, and metadata cache. This
                action cannot be undone.
              </Typography>
              <Typography sx={{ mt: 1 }}>Continue?</Typography>
            </DialogContent>
            <DialogActions>
              <Button onClick={() => setFactoryResetStep(0)}>Cancel</Button>
              <Button
                color="error"
                onClick={() => {
                  setFactoryResetConfirmText('');
                  setFactoryResetStep(2);
                }}
              >
                Continue
              </Button>
            </DialogActions>
          </Dialog>

          {/* Factory Reset Dialog 2: Type RESET */}
          <Dialog
            open={factoryResetStep === 2}
            onClose={() => setFactoryResetStep(0)}
          >
            <DialogTitle>Confirm Factory Reset</DialogTitle>
            <DialogContent>
              <Typography sx={{ mb: 2 }}>
                Type <strong>RESET</strong> to confirm.
              </Typography>
              <TextField
                autoFocus
                fullWidth
                value={factoryResetConfirmText}
                onChange={(e: ChangeEvent<HTMLInputElement>) =>
                  setFactoryResetConfirmText(e.target.value)
                }
                placeholder="Type RESET"
              />
            </DialogContent>
            <DialogActions>
              <Button onClick={() => setFactoryResetStep(0)}>Cancel</Button>
              <Button
                color="error"
                variant="contained"
                disabled={
                  factoryResetConfirmText !== 'RESET' ||
                  factoryResetInProgress
                }
                onClick={async () => {
                  setFactoryResetInProgress(true);
                  try {
                    await api.factoryReset('RESET');
                    localStorage.clear();
                    window.location.href = '/';
                  } catch (err) {
                    setFactoryResetInProgress(false);
                    setFactoryResetStep(0);
                    alert(
                      `Factory reset failed: ${err instanceof Error ? err.message : 'Unknown error'}`
                    );
                  }
                }}
              >
                {factoryResetInProgress ? 'Resetting...' : 'Reset Everything'}
              </Button>
            </DialogActions>
          </Dialog>
        </TabPanel>

        <TabPanel value={tabValue} index={10}>
          <TempLoginTab />
        </TabPanel>

        <Box
          sx={{
            position: 'sticky',
            bottom: 0,
            p: 2,
            display: 'flex',
            gap: 2,
            justifyContent: 'flex-end',
            borderTop: 1,
            borderColor: 'divider',
            bgcolor: 'background.paper',
            zIndex: 10,
            boxShadow: '0 -2px 8px rgba(0,0,0,0.3)',
          }}
        >
          <input
            type="file"
            accept="application/json"
            ref={importInputRef}
            onChange={handleImportFileChange}
            style={{ display: 'none' }}
          />
          <Button
            variant="outlined"
            onClick={handleExportSettings}
            disabled={exportInProgress}
          >
            {exportInProgress ? 'Exporting...' : 'Export Settings'}
          </Button>
          <Button
            variant="outlined"
            onClick={handleImportClick}
            disabled={importInProgress}
          >
            {importInProgress ? 'Importing...' : 'Import Settings'}
          </Button>
          <Button
            variant="outlined"
            startIcon={<RestartAltIcon />}
            onClick={handleReset}
          >
            Reset to Defaults
          </Button>
          <Button
            variant="contained"
            startIcon={<SaveIcon />}
            onClick={handleSave}
            disabled={!configLoaded}
          >
            Save Settings
          </Button>
        </Box>
      </Paper>

      {/* Floating save/cancel panel — visible when there are unsaved changes */}
      {hasUnsavedChanges && (
        <Paper
          elevation={6}
          sx={{
            position: 'fixed',
            bottom: 24,
            right: 24,
            zIndex: 1300,
            display: 'flex',
            alignItems: 'center',
            gap: 1.5,
            px: 2.5,
            py: 1.5,
            borderRadius: 3,
            bgcolor: 'background.paper',
            boxShadow: 6,
          }}
        >
          <SaveIcon fontSize="small" color="primary" sx={{ mr: 0.5 }} />
          <Button
            size="small"
            onClick={() => {
              if (savedSnapshot) {
                const prev = JSON.parse(savedSnapshot) as SettingsState;
                setSettings(prev);
              }
            }}
          >
            Discard
          </Button>
          <Button
            size="small"
            variant="contained"
            onClick={handleSave}
            disabled={!configLoaded}
          >
            Save
          </Button>
        </Paper>
      )}

      {/* Library Path Browser Dialog */}
      <Dialog
        open={browserOpen}
        onClose={handleBrowserCancel}
        maxWidth="md"
        fullWidth
      >
        <DialogTitle>Browse Server Filesystem</DialogTitle>
        <DialogContent>
          <Typography variant="body2" color="text.secondary" gutterBottom>
            Select the library path where organized audiobooks will be stored.
          </Typography>
          <Box sx={{ mt: 2 }}>
            <ServerFileBrowser
              initialPath={selectedPath || settings.libraryPath}
              onSelect={handleBrowserSelect}
              showFiles={false}
              allowDirSelect={true}
              allowFileSelect={false}
            />
          </Box>
          {selectedPath && (
            <Alert severity="info" sx={{ mt: 2 }}>
              <Typography variant="body2">
                <strong>Selected:</strong> {selectedPath}
              </Typography>
            </Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={handleBrowserCancel}>Cancel</Button>
          <Button
            onClick={handleBrowserConfirm}
            variant="contained"
            disabled={!selectedPath}
          >
            Select Folder
          </Button>
        </DialogActions>
      </Dialog>

      {/* Import Folder Dialog */}
      <Dialog
        open={addFolderDialogOpen}
        onClose={() => setAddFolderDialogOpen(false)}
        maxWidth="md"
        fullWidth
      >
        <DialogTitle>Add Import Path (Watch Location)</DialogTitle>
        <DialogContent>
          <Alert severity="info" sx={{ mb: 2 }}>
            <strong>Import paths</strong> are separate from your main library.
            They are watched for new audiobook files that will be scanned,
            organized, and moved to your library path.
          </Alert>

          {!showFolderBrowser ? (
            <Box>
              <TextField
                autoFocus
                fullWidth
                label="Folder Path"
                value={newFolderPath}
                onChange={(e) => setNewFolderPath(e.target.value)}
                placeholder="/path/to/downloads"
                sx={{ mt: 1 }}
              />
              <Button
                startIcon={<FolderOpenIcon />}
                onClick={() => setShowFolderBrowser(true)}
                sx={{ mt: 2 }}
              >
                Browse Server Filesystem
              </Button>
            </Box>
          ) : (
            <Box>
              <Button
                onClick={() => setShowFolderBrowser(false)}
                sx={{ mb: 2 }}
              >
                ← Back to Manual Entry
              </Button>
              <ServerFileBrowser
                initialPath={newFolderPath || '/'}
                onSelect={handleFolderBrowserSelect}
                showFiles={false}
                allowDirSelect={true}
                allowFileSelect={false}
              />
              {newFolderPath && (
                <Alert severity="success" sx={{ mt: 2 }}>
                  <Typography variant="body2">
                    <strong>Selected:</strong> {newFolderPath}
                  </Typography>
                </Alert>
              )}
            </Box>
          )}
        </DialogContent>
        <DialogActions>
          <Button
            onClick={() => {
              setAddFolderDialogOpen(false);
              setShowFolderBrowser(false);
            }}
          >
            Cancel
          </Button>
          <Button
            onClick={handleAddImportFolder}
            variant="contained"
            disabled={!newFolderPath.trim()}
          >
            Add Path
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog
        open={Boolean(cancelScanTarget)}
        onClose={() => setCancelScanTarget(null)}
      >
        <DialogTitle>Cancel Scan</DialogTitle>
        <DialogContent>
          <Typography variant="body2" gutterBottom>
            Cancel scan for{' '}
            <strong>{cancelScanTarget?.path || 'this folder'}</strong>?
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setCancelScanTarget(null)}>
            Keep Scanning
          </Button>
          <Button
            color="error"
            variant="contained"
            onClick={handleConfirmCancelScan}
          >
            Cancel Scan
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog
        open={Boolean(scanErrorTarget)}
        onClose={() => setScanErrorTarget(null)}
      >
        <DialogTitle>Scan Errors</DialogTitle>
        <DialogContent>
          <Typography variant="body2" gutterBottom>
            Errors while scanning{' '}
            <strong>{scanErrorTarget?.path || 'this folder'}</strong>:
          </Typography>
          {scanErrorTarget?.errors?.length ? (
            <List dense>
              {scanErrorTarget.errors.map((error, index) => (
                <ListItem key={`${error}-${index}`}>
                  <ListItemText primary={error} />
                </ListItem>
              ))}
            </List>
          ) : (
            <Typography variant="body2" color="text.secondary">
              No errors recorded.
            </Typography>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setScanErrorTarget(null)}>Close</Button>
        </DialogActions>
      </Dialog>

      <Dialog
        open={restoreDialogOpen}
        onClose={() => setRestoreDialogOpen(false)}
      >
        <DialogTitle>Restore Backup</DialogTitle>
        <DialogContent>
          <Alert severity="warning" sx={{ mb: 2 }}>
            This will replace the current database with the selected backup.
          </Alert>
          <Typography variant="body2" gutterBottom>
            Restore from{' '}
            <strong>{restoreTarget?.filename || 'selected backup'}</strong>?
          </Typography>
          <FormControlLabel
            control={
              <Switch
                checked={restoreVerify}
                onChange={(e) => setRestoreVerify(e.target.checked)}
              />
            }
            label="Verify backup before restore"
          />
        </DialogContent>
        <DialogActions>
          <Button
            onClick={() => setRestoreDialogOpen(false)}
            disabled={restoreInProgress}
          >
            Cancel
          </Button>
          <Button
            variant="contained"
            onClick={handleConfirmRestore}
            disabled={restoreInProgress}
          >
            {restoreInProgress ? 'Restoring...' : 'Restore'}
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog
        open={Boolean(deleteBackupTarget)}
        onClose={() => setDeleteBackupTarget(null)}
      >
        <DialogTitle>Delete Backup</DialogTitle>
        <DialogContent>
          <Typography variant="body2">
            Delete{' '}
            <strong>{deleteBackupTarget?.filename || 'this backup'}</strong>?
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button
            onClick={() => setDeleteBackupTarget(null)}
            disabled={deleteBackupInProgress}
          >
            Cancel
          </Button>
          <Button
            variant="contained"
            color="error"
            onClick={handleConfirmDeleteBackup}
            disabled={deleteBackupInProgress}
          >
            {deleteBackupInProgress ? 'Deleting...' : 'Delete'}
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog
        open={importDialogOpen}
        onClose={() => setImportDialogOpen(false)}
        maxWidth="sm"
        fullWidth
      >
        <DialogTitle>Import Settings</DialogTitle>
        <DialogContent>
          <Alert severity="warning" sx={{ mb: 2 }}>
            Importing settings will overwrite your current configuration.
          </Alert>
          <Typography variant="body2" gutterBottom>
            Import from <strong>{importFileName || 'selected file'}</strong>?
          </Typography>
          {importKeys.length > 0 && (
            <List dense>
              {importKeys.slice(0, 12).map((key) => (
                <ListItem key={key}>
                  <ListItemText primary={key} />
                </ListItem>
              ))}
              {importKeys.length > 12 && (
                <ListItem>
                  <ListItemText
                    primary={`+${importKeys.length - 12} more fields`}
                  />
                </ListItem>
              )}
            </List>
          )}
        </DialogContent>
        <DialogActions>
          <Button
            onClick={() => setImportDialogOpen(false)}
            disabled={importInProgress}
          >
            Cancel
          </Button>
          <Button
            variant="contained"
            onClick={handleConfirmImport}
            disabled={importInProgress}
          >
            {importInProgress ? 'Importing...' : 'Import Settings'}
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog
        open={blocker.state === 'blocked'}
        onClose={handleCancelNavigation}
      >
        <DialogTitle>Unsaved Changes</DialogTitle>
        <DialogContent>
          <Typography variant="body2" gutterBottom>
            You have unsaved changes. Save before leaving?
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={handleCancelNavigation}>Cancel</Button>
          <Button onClick={handleDiscardNavigation}>Discard</Button>
          <Button variant="contained" onClick={handleSaveAndNavigate}>
            Save
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
