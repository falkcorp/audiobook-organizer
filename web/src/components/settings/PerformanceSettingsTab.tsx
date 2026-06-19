// file: web/src/components/settings/PerformanceSettingsTab.tsx
// version: 1.0.0
// guid: a7b8c9d0-e1f2-3456-abcd-567890123456
// last-edited: 2026-06-19

import {
  Grid,
  Typography,
  Divider,
  TextField,
  Switch,
  FormControlLabel,
  MenuItem,
  InputAdornment,
  Radio,
  RadioGroup,
  FormControl,
  FormLabel,
} from '@mui/material';

interface PerformanceSettingsTabProps {
  settings: {
    concurrentScans: number;
    memoryLimitType: string;
    cacheSize: number;
    cacheInvalidateOnBookUpdate: boolean;
    metadataFetchCacheTTLDays: number;
    memoryLimitPercent: number;
    memoryLimitMB: number;
    purgeSoftDeletedAfterDays: number;
    purgeSoftDeletedDeleteFiles: boolean;
    logLevel: string;
    logFormat: string;
    enableJsonLogging: boolean;
  };
  handleChange: (field: string, value: string | boolean | number | string[]) => void;
}

export function PerformanceSettingsTab({ settings, handleChange }: PerformanceSettingsTabProps) {
  return (
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
  );
}
