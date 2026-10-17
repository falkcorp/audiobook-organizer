// file: web/src/components/settings/PerformanceSettingsTab.tsx
// version: 1.1.2
// guid: a7b8c9d0-e1f2-3456-abcd-567890123456
// last-edited: 2026-10-17

import {
  Grid,
  Typography,
  Divider,
  TextField,
  Switch,
  FormControlLabel,
  MenuItem,
} from '@mui/material';

interface PerformanceSettingsTabProps {
  settings: {
    concurrentScans: number;
    cacheInvalidateOnBookUpdate: boolean;
    metadataFetchCacheTTLDays: number;
    purgeSoftDeletedAfterDays: number;
    backupRetentionDays: number;
    purgeSoftDeletedDeleteFiles: boolean;
    logLevel: string;
  };
  handleChange: (field: string, value: string | boolean | number | string[]) => void;
}

export function PerformanceSettingsTab({ settings, handleChange }: PerformanceSettingsTabProps) {
  return (
    <Grid container spacing={3}>
      <Grid size={12}>
        <Typography variant="h6" gutterBottom>
          Performance Settings
        </Typography>
        <Divider sx={{ mb: 2 }} />
      </Grid>

      <Grid
        size={{
          xs: 12,
          sm: 6,
        }}
      >
        <TextField
          fullWidth
          type="number"
          label="Concurrent Scans"
          value={settings.concurrentScans}
          onChange={(e) => handleChange('concurrentScans', parseInt(e.target.value) || 1)}
          helperText="Number of folders to scan simultaneously"
          slotProps={{
            htmlInput: { min: 1, max: 16 },
          }}
        />
      </Grid>

      <Grid size={12}>
        <Typography variant="subtitle1" gutterBottom>
          Caching
        </Typography>
      </Grid>

      <Grid
        size={{
          xs: 12,
          sm: 6,
        }}
      >
        <TextField
          fullWidth
          type="number"
          label="Metadata fetch cache TTL (days)"
          value={settings.metadataFetchCacheTTLDays}
          onChange={(e) => handleChange('metadataFetchCacheTTLDays', parseInt(e.target.value) || 0)}
          helperText="How long to keep Audible/Audnexus API results before re-fetching. 0 = never expire."
          slotProps={{
            htmlInput: { min: 0, max: 365 },
          }}
        />
      </Grid>

      <Grid size={12}>
        <FormControlLabel
          control={
            <Switch
              checked={settings.cacheInvalidateOnBookUpdate}
              onChange={(e) => handleChange('cacheInvalidateOnBookUpdate', e.target.checked)}
            />
          }
          label="Invalidate list cache on book update"
        />
        <Typography
          variant="caption"
          sx={{
            color: 'text.secondary',
            display: 'block',
          }}
        >
          When off (default), metadata fetches and write-back operations keep the library list cache
          warm. Turn on only if you need the library page to reflect every individual book update
          immediately.
        </Typography>
      </Grid>

      <Grid size={12}>
        <Divider sx={{ my: 2 }} />
        <Typography variant="subtitle1" gutterBottom>
          Lifecycle &amp; Retention
        </Typography>
        <Typography
          variant="body2"
          sx={{
            color: 'text.secondary',
            mb: 1,
          }}
        >
          Control how long soft-deleted books remain before automatic purge runs.
        </Typography>
      </Grid>

      <Grid
        size={{
          xs: 12,
          sm: 6,
        }}
      >
        <TextField
          fullWidth
          type="number"
          label="Auto-Purge After (days)"
          value={settings.purgeSoftDeletedAfterDays}
          onChange={(e) => handleChange('purgeSoftDeletedAfterDays', parseInt(e.target.value) || 0)}
          helperText="Set to 0 to disable automatic purge"
          slotProps={{
            htmlInput: { min: 0, max: 365 },
          }}
        />
      </Grid>
      <Grid
        size={{
          xs: 12,
          sm: 6,
        }}
      >
        <TextField
          fullWidth
          type="number"
          label="Backup retention (days)"
          value={settings.backupRetentionDays}
          onChange={(e) => handleChange('backupRetentionDays', parseInt(e.target.value) || 0)}
          helperText="0 = same as soft-delete retention"
          slotProps={{
            htmlInput: { min: 0, max: 365 },
          }}
        />
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
              checked={settings.purgeSoftDeletedDeleteFiles}
              onChange={(e) => handleChange('purgeSoftDeletedDeleteFiles', e.target.checked)}
            />
          }
          label="Delete files from disk during purge"
        />
        <Typography
          variant="caption"
          sx={{
            color: 'text.secondary',
            display: 'block',
            ml: 4,
          }}
        >
          Disable to keep files on disk while clearing database records.
        </Typography>
      </Grid>

      <Grid size={12}>
        <Divider sx={{ my: 2 }} />
        <Typography variant="subtitle1" gutterBottom>
          Logging
        </Typography>
      </Grid>

      <Grid
        size={{
          xs: 12,
          sm: 6,
        }}
      >
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
    </Grid>
  );
}
