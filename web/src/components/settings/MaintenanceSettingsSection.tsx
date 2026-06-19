// file: web/src/components/settings/MaintenanceSettingsSection.tsx
// version: 1.0.0
// guid: d4e5f6a7-b8c9-0123-defa-234567890123
// last-edited: 2026-06-19

import {
  Box,
  Typography,
  TextField,
  FormControlLabel,
  Switch,
  Grid,
  Divider,
  MenuItem,
} from '@mui/material';
import * as api from '../../services/api';

interface MaintenanceSettingsSectionProps {
  config: api.MaintenanceConfig;
  onChange: (patch: Partial<api.MaintenanceConfig>) => void;
}

const hourOptions = Array.from({ length: 24 }, (_, i) => i);

const TASK_TOGGLES: Array<{ field: keyof api.MaintenanceConfig; label: string }> = [
  { field: 'dedup_refresh', label: 'Dedup refresh' },
  { field: 'series_prune', label: 'Series prune' },
  { field: 'author_split', label: 'Author split' },
  { field: 'tombstone_cleanup', label: 'Tombstone cleanup' },
  { field: 'reconcile', label: 'Reconcile' },
  { field: 'purge_deleted', label: 'Purge deleted' },
  { field: 'purge_old_logs', label: 'Purge old logs' },
  { field: 'db_optimize', label: 'DB optimize' },
  { field: 'library_scan', label: 'Library scan' },
  { field: 'library_organize', label: 'Library organize' },
  { field: 'metadata_refresh', label: 'Metadata refresh' },
  { field: 'library_size_refresh', label: 'Library size refresh' },
  { field: 'acoustid_online_lookup', label: 'AcoustID online lookup' },
];

export function MaintenanceSettingsSection({ config, onChange }: MaintenanceSettingsSectionProps) {
  return (
    <Box>
      <Typography variant="h6" gutterBottom>
        Maintenance Window
      </Typography>

      <Grid container spacing={2}>
        <Grid item xs={12}>
          <FormControlLabel
            control={
              <Switch
                checked={config.enabled}
                onChange={(e) => onChange({ enabled: e.target.checked })}
              />
            }
            label="Enable nightly maintenance window"
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            select
            fullWidth
            label="Window start (hour)"
            value={config.window_start}
            onChange={(e) => onChange({ window_start: Number(e.target.value) })}
            size="small"
          >
            {hourOptions.map((h) => (
              <MenuItem key={h} value={h}>
                {String(h).padStart(2, '0')}:00
              </MenuItem>
            ))}
          </TextField>
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            select
            fullWidth
            label="Window end (hour)"
            value={config.window_end}
            onChange={(e) => onChange({ window_end: Number(e.target.value) })}
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
          <Divider sx={{ my: 1 }} />
          <Typography variant="subtitle2" gutterBottom>
            Nightly Tasks
          </Typography>
        </Grid>

        {TASK_TOGGLES.map(({ field, label }) => (
          <Grid item xs={12} sm={6} key={field}>
            <FormControlLabel
              control={
                <Switch
                  checked={config[field] as boolean}
                  onChange={(e) => onChange({ [field]: e.target.checked })}
                />
              }
              label={label}
            />
          </Grid>
        ))}

        <Grid item xs={12} sm={6}>
          <TextField
            fullWidth
            type="number"
            label="AcoustID nightly limit"
            value={config.acoustid_nightly_limit}
            onChange={(e) => onChange({ acoustid_nightly_limit: Number(e.target.value) })}
            helperText="Max AcoustID lookups per maintenance window"
            size="small"
            inputProps={{ min: 0 }}
          />
        </Grid>
      </Grid>
    </Box>
  );
}
