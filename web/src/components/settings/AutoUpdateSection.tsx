// file: web/src/components/settings/AutoUpdateSection.tsx
// version: 1.1.0
// guid: f8e2d4c6-b3a1-4f7e-9c2d-5a8b6e3f1d90
// last-edited: 2026-11-05

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
import type { SettingsState } from '../../pages/Settings';

interface AutoUpdateSectionProps {
  settings: {
    autoUpdateEnabled: boolean;
    autoUpdateChannel: string;
    autoUpdateCheckMinutes: number;
    autoUpdateWindowStart: number;
    autoUpdateWindowEnd: number;
  };
  setSettings: React.Dispatch<React.SetStateAction<SettingsState>>;
}

/** Release-note sentence of the R0 updater hotfix: unattended runs only check. */
const AUTO_APPLY_PAUSED_MESSAGE =
  'Automatic updates now only check for a new release and notify you; apply updates from Settings.';

/** Shown when the running binary is a local build (a git describe suffix). */
const LOCAL_BUILD_MESSAGE = 'Local build, not compared with releases.';

export function AutoUpdateSection({ settings, setSettings }: AutoUpdateSectionProps) {
  const [updateInfo, setUpdateInfo] = useState<api.UpdateInfo | null>(null);
  const [checking, setChecking] = useState(false);
  const [applying, setApplying] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirmOpen, setConfirmOpen] = useState(false);
  // A local build is never compared with releases, so the operator names the
  // version to apply; the server refuses an unnamed apply on a local build.
  const [applyVersion, setApplyVersion] = useState('');

  const localBuild = updateInfo?.local_build === true;
  const namedVersion = applyVersion.trim();
  const canApply = localBuild ? namedVersion !== '' : updateInfo?.update_available === true;
  const applyTarget = localBuild ? namedVersion : updateInfo?.latest_version;
  // The setting asks for automatic apply, which this build pauses. The saved
  // setting comes back from the server; the switch shows an unsaved change.
  const autoApplyPaused = settings.autoUpdateEnabled || updateInfo?.auto_apply_paused === true;

  useEffect(() => {
    api
      .getUpdateStatus()
      .then(setUpdateInfo)
      .catch((err) => console.error('Failed to get update status:', err));
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
      await api.applyUpdate(localBuild ? namedVersion : undefined);
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
        <Grid size={12}>
          <Typography
            variant="body2"
            sx={{
              color: 'text.secondary',
              mb: 1,
            }}
          >
            Current version: <strong>{updateInfo?.current_version || 'loading...'}</strong>
          </Typography>
          {autoApplyPaused && (
            <Alert severity="info" sx={{ mb: 2 }}>
              {AUTO_APPLY_PAUSED_MESSAGE}
            </Alert>
          )}
          {localBuild && (
            <Alert severity="info" sx={{ mb: 2 }}>
              {LOCAL_BUILD_MESSAGE}
            </Alert>
          )}
          {updateInfo?.update_available && (
            <Alert severity="info" sx={{ mb: 2 }}>
              Update available: {updateInfo.latest_version}
              {updateInfo.release_url && (
                <>
                  {' '}
                  &mdash;{' '}
                  <a href={updateInfo.release_url} target="_blank" rel="noreferrer">
                    Release notes
                  </a>
                </>
              )}
            </Alert>
          )}
          {error && (
            <Alert severity="error" sx={{ mb: 2 }}>
              {error}
            </Alert>
          )}
        </Grid>

        <Grid
          size={{
            xs: 12,
            sm: 6,
          }}
        >
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

        <Grid
          size={{
            xs: 12,
            sm: 6,
          }}
        >
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

        <Grid
          size={{
            xs: 12,
            sm: 4,
          }}
        >
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
            slotProps={{
              htmlInput: { min: 1 },
            }}
          />
        </Grid>

        <Grid
          size={{
            xs: 12,
            sm: 4,
          }}
        >
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

        <Grid
          size={{
            xs: 12,
            sm: 4,
          }}
        >
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

        {localBuild && (
          <Grid
            size={{
              xs: 12,
              sm: 6,
            }}
          >
            <TextField
              fullWidth
              label="Version to apply"
              placeholder="v0.0.0"
              helperText="A local build applies only the release you name."
              value={applyVersion}
              onChange={(e) => setApplyVersion(e.target.value)}
              size="small"
            />
          </Grid>
        )}

        <Grid size={12}>
          <Stack
            direction="row"
            spacing={2}
            sx={{
              alignItems: 'center',
            }}
          >
            <Button variant="outlined" onClick={handleCheck} disabled={checking}>
              {checking ? <CircularProgress size={20} sx={{ mr: 1 }} /> : null}
              Check Now
            </Button>
            {canApply && (
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
              <Typography
                variant="caption"
                sx={{
                  color: 'text.secondary',
                }}
              >
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
            This will download and apply the update to version <strong>{applyTarget}</strong>, then
            restart the server. The page will be temporarily unavailable.
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
