// file: web/src/components/settings/ITunesTransfer.tsx
// version: 1.0.2
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  CardHeader,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Divider,
  IconButton,
  LinearProgress,
  List,
  ListItem,
  ListItemIcon,
  ListItemSecondaryAction,
  ListItemText,
  Stack,
  Tooltip,
  Typography,
} from '@mui/material';
import CloudDownloadIcon from '@mui/icons-material/CloudDownload';
import CloudUploadIcon from '@mui/icons-material/CloudUpload';
import RestoreIcon from '@mui/icons-material/Restore';
import HistoryIcon from '@mui/icons-material/History';
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import ErrorIcon from '@mui/icons-material/Error';
import InsertDriveFileIcon from '@mui/icons-material/InsertDriveFile';
import { useToast } from '../toast/ToastProvider';

// --- API helpers (talk directly to the endpoints we just shipped) -----------

const API_BASE = '/api/v1/itunes/library';

interface UploadResponse {
  valid: boolean;
  installed: boolean;
  tracks: number;
  playlists: number;
  version: string;
  error?: string;
}

interface BackupEntry {
  name: string;
  size: number;
  timestamp: string;
}

interface BackupListResponse {
  backups: BackupEntry[];
  count: number;
}

interface RestoreResponse {
  restored: boolean;
  tracks: number;
  playlists: number;
  version: string;
}

async function downloadITL(): Promise<void> {
  const resp = await fetch(`${API_BASE}/download`);
  if (!resp.ok) {
    const body = await resp.json().catch(() => ({ error: resp.statusText }));
    throw new Error(body.error || `Download failed: ${resp.status}`);
  }
  const blob = await resp.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = 'iTunes Library.itl';
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

async function uploadITL(file: File, install: boolean): Promise<UploadResponse> {
  const form = new FormData();
  form.append('library', file);
  const resp = await fetch(`${API_BASE}/upload?install=${install}`, { method: 'POST', body: form });
  const body = await resp.json();
  if (!resp.ok) throw new Error(body.error || `Upload failed: ${resp.status}`);
  return body;
}

async function listBackups(): Promise<BackupListResponse> {
  const resp = await fetch(`${API_BASE}/backups`);
  if (!resp.ok) {
    const body = await resp.json().catch(() => ({ error: resp.statusText }));
    throw new Error(body.error || `Failed to list backups: ${resp.status}`);
  }
  return resp.json();
}

async function restoreBackup(name: string): Promise<RestoreResponse> {
  const resp = await fetch(`${API_BASE}/restore`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ backup_name: name }),
  });
  const body = await resp.json();
  if (!resp.ok) throw new Error(body.error || `Restore failed: ${resp.status}`);
  return body;
}

// --- Component --------------------------------------------------------------

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function formatDate(iso: string): string {
  return new Date(iso).toLocaleString();
}

export function ITunesTransfer() {
  const { toast } = useToast();

  // Download state
  const [downloading, setDownloading] = useState(false);

  // Upload state
  const [uploading, setUploading] = useState(false);
  const [uploadResult, setUploadResult] = useState<UploadResponse | null>(null);
  const [selectedFile, setSelectedFile] = useState<File | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  // Backup state
  const [backups, setBackups] = useState<BackupEntry[]>([]);
  const [backupsLoading, setBackupsLoading] = useState(false);

  // Restore state
  const [restoring, setRestoring] = useState<string | null>(null);
  const [confirmRestore, setConfirmRestore] = useState<string | null>(null);

  // Install confirmation
  const [confirmInstall, setConfirmInstall] = useState(false);

  const loadBackups = useCallback(async () => {
    setBackupsLoading(true);
    try {
      const resp = await listBackups();
      setBackups(resp.backups || []);
    } catch (err) {
      toast(`Failed to load backups: ${err}`, 'error');
    } finally {
      setBackupsLoading(false);
    }
  }, [toast]);

  useEffect(() => {
    loadBackups();
  }, [loadBackups]);

  // --- Download handler ---
  const handleDownload = async () => {
    setDownloading(true);
    try {
      await downloadITL();
      toast('ITL file downloaded', 'success');
    } catch (err) {
      toast(`Download failed: ${err}`, 'error');
    } finally {
      setDownloading(false);
    }
  };

  // --- Upload handlers ---
  const handleFileSelect = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    setSelectedFile(file);
    setUploadResult(null);
  };

  const handleValidate = async () => {
    if (!selectedFile) return;
    setUploading(true);
    setUploadResult(null);
    try {
      const resp = await uploadITL(selectedFile, false);
      setUploadResult(resp);
      if (resp.valid) {
        toast(`Valid ITL: ${resp.tracks} tracks, ${resp.playlists} playlists`, 'success');
      }
    } catch (err) {
      toast(`Validation failed: ${err}`, 'error');
    } finally {
      setUploading(false);
    }
  };

  const handleInstall = async () => {
    if (!selectedFile) return;
    setConfirmInstall(false);
    setUploading(true);
    try {
      const resp = await uploadITL(selectedFile, true);
      setUploadResult(resp);
      if (resp.installed) {
        toast('ITL file installed successfully', 'success');
        setSelectedFile(null);
        if (fileInputRef.current) fileInputRef.current.value = '';
        loadBackups(); // refresh — a backup was created
      }
    } catch (err) {
      toast(`Install failed: ${err}`, 'error');
    } finally {
      setUploading(false);
    }
  };

  // --- Restore handler ---
  const handleRestore = async (name: string) => {
    setConfirmRestore(null);
    setRestoring(name);
    try {
      const resp = await restoreBackup(name);
      toast(
        `Restored: ${resp.tracks} tracks, ${resp.playlists} playlists (v${resp.version})`,
        'success'
      );
      loadBackups(); // refresh — a new backup was created from the current file
    } catch (err) {
      toast(`Restore failed: ${err}`, 'error');
    } finally {
      setRestoring(null);
    }
  };

  return (
    <Card sx={{ mt: 3 }}>
      <CardHeader
        title="ITL File Transfer"
        subheader="Download, upload, or restore iTunes Library files"
      />
      <CardContent>
        <Stack spacing={3}>
          {/* Download Section */}
          <Box>
            <Typography variant="subtitle1" gutterBottom>
              Download Current Library
            </Typography>
            <Typography
              variant="body2"
              sx={{
                color: 'text.secondary',
                mb: 1,
              }}
            >
              Download the current ITL file from the server for backup or editing.
            </Typography>
            <Button
              variant="outlined"
              startIcon={downloading ? <CircularProgress size={18} /> : <CloudDownloadIcon />}
              onClick={handleDownload}
              disabled={downloading}
            >
              {downloading ? 'Downloading...' : 'Download Library'}
            </Button>
          </Box>

          <Divider />

          {/* Upload Section */}
          <Box>
            <Typography variant="subtitle1" gutterBottom>
              Upload Library
            </Typography>
            <Typography
              variant="body2"
              sx={{
                color: 'text.secondary',
                mb: 1,
              }}
            >
              Upload an ITL file to validate or install as the active library. Installing creates an
              automatic backup of the current file.
            </Typography>

            <Stack
              direction="row"
              spacing={2}
              sx={{
                alignItems: 'center',
              }}
            >
              <Button variant="outlined" component="label" startIcon={<CloudUploadIcon />}>
                Choose File
                <input
                  ref={fileInputRef}
                  type="file"
                  accept=".itl"
                  hidden
                  onChange={handleFileSelect}
                />
              </Button>

              {selectedFile && (
                <Chip
                  icon={<InsertDriveFileIcon />}
                  label={`${selectedFile.name} (${formatBytes(selectedFile.size)})`}
                  onDelete={() => {
                    setSelectedFile(null);
                    setUploadResult(null);
                    if (fileInputRef.current) fileInputRef.current.value = '';
                  }}
                />
              )}
            </Stack>

            {selectedFile && (
              <Stack direction="row" spacing={2} sx={{ mt: 2 }}>
                <Button
                  variant="contained"
                  color="primary"
                  onClick={handleValidate}
                  disabled={uploading}
                  startIcon={uploading ? <CircularProgress size={18} /> : undefined}
                >
                  Validate
                </Button>
                <Tooltip title="Backs up the current file, then replaces it">
                  <Button
                    variant="contained"
                    color="warning"
                    onClick={() => setConfirmInstall(true)}
                    disabled={uploading}
                  >
                    Install
                  </Button>
                </Tooltip>
              </Stack>
            )}

            {uploadResult && (
              <Alert
                severity={uploadResult.valid ? 'success' : 'error'}
                icon={uploadResult.valid ? <CheckCircleIcon /> : <ErrorIcon />}
                sx={{ mt: 2 }}
              >
                {uploadResult.valid ? (
                  <>
                    <strong>{uploadResult.installed ? 'Installed' : 'Valid'}</strong>
                    {' — '}
                    {uploadResult.tracks} tracks, {uploadResult.playlists} playlists (v
                    {uploadResult.version})
                  </>
                ) : (
                  uploadResult.error || 'Invalid ITL file'
                )}
              </Alert>
            )}
          </Box>

          <Divider />

          {/* Backups Section */}
          <Box>
            <Stack
              direction="row"
              sx={{
                justifyContent: 'space-between',
                alignItems: 'center',
              }}
            >
              <Typography variant="subtitle1">
                <HistoryIcon fontSize="small" sx={{ verticalAlign: 'middle', mr: 0.5 }} />
                Backups
              </Typography>
              <Button size="small" onClick={loadBackups} disabled={backupsLoading}>
                Refresh
              </Button>
            </Stack>

            {backupsLoading && <LinearProgress sx={{ mt: 1 }} />}

            {!backupsLoading && backups.length === 0 && (
              <Typography
                variant="body2"
                sx={{
                  color: 'text.secondary',
                  mt: 1,
                }}
              >
                No backups found. Backups are created automatically when uploading or restoring.
              </Typography>
            )}

            {backups.length > 0 && (
              <List dense sx={{ mt: 1 }}>
                {backups.map((backup) => (
                  <ListItem key={backup.name}>
                    <ListItemIcon>
                      <InsertDriveFileIcon fontSize="small" />
                    </ListItemIcon>
                    <ListItemText
                      primary={backup.name}
                      secondary={`${formatBytes(backup.size)} — ${formatDate(backup.timestamp)}`}
                    />
                    <ListItemSecondaryAction>
                      <Tooltip title="Restore this backup">
                        <IconButton
                          edge="end"
                          onClick={() => setConfirmRestore(backup.name)}
                          disabled={restoring !== null}
                        >
                          {restoring === backup.name ? (
                            <CircularProgress size={20} />
                          ) : (
                            <RestoreIcon />
                          )}
                        </IconButton>
                      </Tooltip>
                    </ListItemSecondaryAction>
                  </ListItem>
                ))}
              </List>
            )}
          </Box>
        </Stack>
      </CardContent>

      {/* Confirm Install Dialog */}
      <Dialog open={confirmInstall} onClose={() => setConfirmInstall(false)}>
        <DialogTitle>Install Uploaded Library?</DialogTitle>
        <DialogContent>
          <Typography>
            This will back up the current ITL file and replace it with the uploaded file. The
            current file can be restored from the backups list.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmInstall(false)}>Cancel</Button>
          <Button variant="contained" color="warning" onClick={handleInstall}>
            Install
          </Button>
        </DialogActions>
      </Dialog>

      {/* Confirm Restore Dialog */}
      <Dialog open={confirmRestore !== null} onClose={() => setConfirmRestore(null)}>
        <DialogTitle>Restore Backup?</DialogTitle>
        <DialogContent>
          <Typography>
            This will back up the current ITL file and replace it with{' '}
            <strong>{confirmRestore}</strong>. The backup validates before restoring.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmRestore(null)}>Cancel</Button>
          <Button
            variant="contained"
            color="warning"
            onClick={() => confirmRestore && handleRestore(confirmRestore)}
          >
            Restore
          </Button>
        </DialogActions>
      </Dialog>
    </Card>
  );
}
