// file: web/src/components/settings/ScheduledTasksSection.tsx
// version: 1.0.0
// guid: e5f6a7b8-c9d0-1234-efab-345678901234
// last-edited: 2026-06-19

import {
  Box,
  Typography,
  TextField,
  FormControlLabel,
  Switch,
  Grid,
  Paper,
} from '@mui/material';
import * as api from '../../services/api';

interface ScheduledTasksSectionProps {
  config: api.ScheduledTasksConfig;
  onChange: (patch: Partial<api.ScheduledTasksConfig>) => void;
}

interface TaskRowProps {
  label: string;
  enabled: boolean;
  interval: number;
  onStartup?: boolean;
  hasOnStartup: boolean;
  onEnabledChange: (val: boolean) => void;
  onIntervalChange: (val: number) => void;
  onStartupChange?: (val: boolean) => void;
}

function TaskRow({
  label,
  enabled,
  interval,
  onStartup,
  hasOnStartup,
  onEnabledChange,
  onIntervalChange,
  onStartupChange,
}: TaskRowProps) {
  return (
    <Paper variant="outlined" sx={{ p: 2 }}>
      <Typography variant="subtitle2" gutterBottom>
        {label}
      </Typography>
      <Grid container spacing={2} alignItems="center">
        <Grid item xs={12} sm={4}>
          <FormControlLabel
            control={
              <Switch
                checked={enabled}
                onChange={(e) => onEnabledChange(e.target.checked)}
              />
            }
            label="Enabled"
          />
        </Grid>
        <Grid item xs={12} sm={4}>
          <TextField
            fullWidth
            type="number"
            label="Interval (minutes)"
            value={interval}
            onChange={(e) => onIntervalChange(Number(e.target.value))}
            size="small"
            inputProps={{ min: 1 }}
          />
        </Grid>
        {hasOnStartup && onStartupChange && (
          <Grid item xs={12} sm={4}>
            <FormControlLabel
              control={
                <Switch
                  checked={onStartup ?? false}
                  onChange={(e) => onStartupChange(e.target.checked)}
                />
              }
              label="On startup"
            />
          </Grid>
        )}
      </Grid>
    </Paper>
  );
}

export function ScheduledTasksSection({ config, onChange }: ScheduledTasksSectionProps) {
  return (
    <Box>
      <Typography variant="h6" gutterBottom>
        Scheduled Tasks
      </Typography>

      <Grid container spacing={2}>
        <Grid item xs={12}>
          <TaskRow
            label="Dedup Refresh"
            enabled={config.dedup_refresh.enabled}
            interval={config.dedup_refresh.interval}
            onStartup={config.dedup_refresh.on_startup}
            hasOnStartup
            onEnabledChange={(v) =>
              onChange({ dedup_refresh: { ...config.dedup_refresh, enabled: v } })
            }
            onIntervalChange={(v) =>
              onChange({ dedup_refresh: { ...config.dedup_refresh, interval: v } })
            }
            onStartupChange={(v) =>
              onChange({ dedup_refresh: { ...config.dedup_refresh, on_startup: v } })
            }
          />
        </Grid>

        <Grid item xs={12}>
          <TaskRow
            label="Author Split"
            enabled={config.author_split.enabled}
            interval={config.author_split.interval}
            onStartup={config.author_split.on_startup}
            hasOnStartup
            onEnabledChange={(v) =>
              onChange({ author_split: { ...config.author_split, enabled: v } })
            }
            onIntervalChange={(v) =>
              onChange({ author_split: { ...config.author_split, interval: v } })
            }
            onStartupChange={(v) =>
              onChange({ author_split: { ...config.author_split, on_startup: v } })
            }
          />
        </Grid>

        <Grid item xs={12}>
          <TaskRow
            label="DB Optimize"
            enabled={config.db_optimize.enabled}
            interval={config.db_optimize.interval}
            onStartup={config.db_optimize.on_startup}
            hasOnStartup
            onEnabledChange={(v) =>
              onChange({ db_optimize: { ...config.db_optimize, enabled: v } })
            }
            onIntervalChange={(v) =>
              onChange({ db_optimize: { ...config.db_optimize, interval: v } })
            }
            onStartupChange={(v) =>
              onChange({ db_optimize: { ...config.db_optimize, on_startup: v } })
            }
          />
        </Grid>

        <Grid item xs={12}>
          <TaskRow
            label="Metadata Refresh"
            enabled={config.metadata_refresh.enabled}
            interval={config.metadata_refresh.interval}
            onStartup={config.metadata_refresh.on_startup}
            hasOnStartup
            onEnabledChange={(v) =>
              onChange({ metadata_refresh: { ...config.metadata_refresh, enabled: v } })
            }
            onIntervalChange={(v) =>
              onChange({ metadata_refresh: { ...config.metadata_refresh, interval: v } })
            }
            onStartupChange={(v) =>
              onChange({ metadata_refresh: { ...config.metadata_refresh, on_startup: v } })
            }
          />
        </Grid>

        <Grid item xs={12}>
          <TaskRow
            label="Resolve Production Authors"
            enabled={config.resolve_production_authors.enabled}
            interval={config.resolve_production_authors.interval}
            hasOnStartup={false}
            onEnabledChange={(v) =>
              onChange({
                resolve_production_authors: {
                  ...config.resolve_production_authors,
                  enabled: v,
                },
              })
            }
            onIntervalChange={(v) =>
              onChange({
                resolve_production_authors: {
                  ...config.resolve_production_authors,
                  interval: v,
                },
              })
            }
          />
        </Grid>

        <Grid item xs={12}>
          <TaskRow
            label="Series Prune"
            enabled={config.series_prune.enabled}
            interval={config.series_prune.interval}
            onStartup={config.series_prune.on_startup}
            hasOnStartup
            onEnabledChange={(v) =>
              onChange({ series_prune: { ...config.series_prune, enabled: v } })
            }
            onIntervalChange={(v) =>
              onChange({ series_prune: { ...config.series_prune, interval: v } })
            }
            onStartupChange={(v) =>
              onChange({ series_prune: { ...config.series_prune, on_startup: v } })
            }
          />
        </Grid>

        <Grid item xs={12}>
          <TaskRow
            label="AI Dedup Batch"
            enabled={config.ai_dedup_batch.enabled}
            interval={config.ai_dedup_batch.interval}
            onStartup={config.ai_dedup_batch.on_startup}
            hasOnStartup
            onEnabledChange={(v) =>
              onChange({ ai_dedup_batch: { ...config.ai_dedup_batch, enabled: v } })
            }
            onIntervalChange={(v) =>
              onChange({ ai_dedup_batch: { ...config.ai_dedup_batch, interval: v } })
            }
            onStartupChange={(v) =>
              onChange({ ai_dedup_batch: { ...config.ai_dedup_batch, on_startup: v } })
            }
          />
        </Grid>

        <Grid item xs={12}>
          <TaskRow
            label="Reconcile"
            enabled={config.reconcile.enabled}
            interval={config.reconcile.interval}
            onStartup={config.reconcile.on_startup}
            hasOnStartup
            onEnabledChange={(v) =>
              onChange({ reconcile: { ...config.reconcile, enabled: v } })
            }
            onIntervalChange={(v) =>
              onChange({ reconcile: { ...config.reconcile, interval: v } })
            }
            onStartupChange={(v) =>
              onChange({ reconcile: { ...config.reconcile, on_startup: v } })
            }
          />
        </Grid>
      </Grid>
    </Box>
  );
}
