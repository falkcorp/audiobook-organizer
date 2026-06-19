// file: web/src/components/settings/AutoUpdateSection.tsx
// version: 1.0.0
// guid: f8e2d4c6-b3a1-4f7e-9c2d-5a8b6e3f1d90
// last-edited: 2026-06-19

import { useState, useEffect } from 'react';
import {
  Paper,
  Typography,
  Grid,
  Alert,
  FormControlLabel,
  Switch,
  TextField,
  MenuItem,
  Button,
  Stack,
  CircularProgress,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
} from '@mui/material';
import * as api from '../../services/api';

interface AutoUpdateSectionProps {
  settings: {
    autoUpdateEnabled: boolean;
    autoUpdateChannel: string;
    autoUpdateCheckMinutes: number;
    autoUpdateWindowStart: number;
    autoUpdateWindowEnd: number;
  };
  setSettings: React.Dispatch<React.SetStateAction<any>>;
}

export function AutoUpdateSection({ settings, setSettings }: AutoUpdateSectionProps) {
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
                  setSettings((prev: any) => ({
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
              setSettings((prev: any) => ({
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
              setSettings((prev: any) => ({
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
              setSettings((prev: any) => ({
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
              setSettings((prev: any) => ({
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
