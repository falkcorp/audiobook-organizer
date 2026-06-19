// file: web/src/components/settings/DedupSettingsSection.tsx
// version: 1.0.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-06-19

import {
  Box,
  Typography,
  TextField,
  FormControlLabel,
  Switch,
  Grid,
  Divider,
} from '@mui/material';
import * as api from '../../services/api';

interface DedupSettingsSectionProps {
  config: api.DedupConfig;
  onChange: (patch: Partial<api.DedupConfig>) => void;
}

export function DedupSettingsSection({ config, onChange }: DedupSettingsSectionProps) {
  return (
    <Box>
      <Typography variant="h6" gutterBottom>
        Deduplication Settings
      </Typography>

      <Grid container spacing={2}>
        <Grid item xs={12} sm={6}>
          <FormControlLabel
            control={
              <Switch
                checked={config.auto_merge_enabled}
                onChange={(e) => onChange({ auto_merge_enabled: e.target.checked })}
              />
            }
            label="Auto-merge certain duplicates"
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <FormControlLabel
            control={
              <Switch
                checked={config.embeddings_enabled}
                onChange={(e) => onChange({ embeddings_enabled: e.target.checked })}
              />
            }
            label="Enable embedding similarity (Layer-2)"
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <FormControlLabel
            control={
              <Switch
                checked={config.llm_auto_merge_high_confidence}
                onChange={(e) => onChange({ llm_auto_merge_high_confidence: e.target.checked })}
              />
            }
            label="LLM auto-merge on high confidence"
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <FormControlLabel
            control={
              <Switch
                checked={config.on_import_via_scheduler}
                onChange={(e) => onChange({ on_import_via_scheduler: e.target.checked })}
              />
            }
            label="Run dedup on each imported book"
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            fullWidth
            type="number"
            label="Book high threshold"
            value={config.book_high_threshold}
            onChange={(e) => onChange({ book_high_threshold: Number(e.target.value) })}
            size="small"
            inputProps={{ min: 0, max: 1, step: 0.01 }}
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            fullWidth
            type="number"
            label="Book low threshold"
            value={config.book_low_threshold}
            onChange={(e) => onChange({ book_low_threshold: Number(e.target.value) })}
            size="small"
            inputProps={{ min: 0, max: 1, step: 0.01 }}
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            fullWidth
            type="number"
            label="Author high threshold"
            value={config.author_high_threshold}
            onChange={(e) => onChange({ author_high_threshold: Number(e.target.value) })}
            size="small"
            inputProps={{ min: 0, max: 1, step: 0.01 }}
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            fullWidth
            type="number"
            label="Author low threshold"
            value={config.author_low_threshold}
            onChange={(e) => onChange({ author_low_threshold: Number(e.target.value) })}
            size="small"
            inputProps={{ min: 0, max: 1, step: 0.01 }}
          />
        </Grid>

        <Grid item xs={12}>
          <TextField
            fullWidth
            label="Review model"
            value={config.review_model}
            onChange={(e) => onChange({ review_model: e.target.value })}
            helperText="OpenAI model name for LLM review"
            size="small"
          />
        </Grid>

        <Grid item xs={12}>
          <Divider sx={{ my: 1 }} />
          <Typography variant="subtitle2" gutterBottom>
            Signal Band Thresholds
          </Typography>
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            fullWidth
            type="number"
            label="Certain band min"
            value={config.signals.band_certain_min}
            onChange={(e) =>
              onChange({ signals: { ...config.signals, band_certain_min: Number(e.target.value) } })
            }
            size="small"
            inputProps={{ min: 0, max: 1, step: 0.01 }}
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            fullWidth
            type="number"
            label="High band min"
            value={config.signals.band_high_min}
            onChange={(e) =>
              onChange({ signals: { ...config.signals, band_high_min: Number(e.target.value) } })
            }
            size="small"
            inputProps={{ min: 0, max: 1, step: 0.01 }}
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            fullWidth
            type="number"
            label="Medium band min"
            value={config.signals.band_medium_min}
            onChange={(e) =>
              onChange({ signals: { ...config.signals, band_medium_min: Number(e.target.value) } })
            }
            size="small"
            inputProps={{ min: 0, max: 1, step: 0.01 }}
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            fullWidth
            type="number"
            label="Review band min"
            value={config.signals.band_review_min}
            onChange={(e) =>
              onChange({ signals: { ...config.signals, band_review_min: Number(e.target.value) } })
            }
            size="small"
            inputProps={{ min: 0, max: 1, step: 0.01 }}
          />
        </Grid>

        <Grid item xs={12}>
          <Divider sx={{ my: 1 }} />
          <Typography variant="subtitle2" gutterBottom>
            Signal Boosts
          </Typography>
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            fullWidth
            type="number"
            label="Duration boost"
            value={config.signals.duration_boost}
            onChange={(e) =>
              onChange({ signals: { ...config.signals, duration_boost: Number(e.target.value) } })
            }
            size="small"
            inputProps={{ min: 0, max: 1, step: 0.01 }}
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            fullWidth
            type="number"
            label="Folder path boost"
            value={config.signals.folder_path_boost}
            onChange={(e) =>
              onChange({ signals: { ...config.signals, folder_path_boost: Number(e.target.value) } })
            }
            size="small"
            inputProps={{ min: 0, max: 1, step: 0.01 }}
          />
        </Grid>
      </Grid>
    </Box>
  );
}
