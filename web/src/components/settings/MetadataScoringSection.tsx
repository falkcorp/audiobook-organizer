// file: web/src/components/settings/MetadataScoringSection.tsx
// version: 1.1.0
// guid: c3d4e5f6-a7b8-9012-cdef-123456789012
// last-edited: 2026-07-11

import {
  Box,
  Typography,
  TextField,
  FormControlLabel,
  Switch,
  Grid,
  Button,
  Divider,
} from '@mui/material';
import * as api from '../../services/api';

interface MetadataScoringProps {
  config: api.MetadataScoringConfig;
  onChange: (patch: Partial<api.MetadataScoringConfig>) => void;
}

// SCORING_DEFAULTS mirrors the Go legacy literals TASK-02 preserves (the
// literal-defaults block in internal/config/config.go, ~1685). Used by the
// per-group "Reset to defaults" buttons. Duration tier VALUE arrays are the
// Multiplier/Score columns of durationTiers in
// internal/metafetch/service_scoring.go (edges stay fixed in code).
const SCORING_DEFAULTS = {
  transcription_title_exact_boost: 2.0,
  transcription_title_substr_boost: 1.4,
  transcription_author_boost: 1.6,
  transcription_narrator_boost: 1.4,
  compilation_penalty: 0.15,
  rich_metadata_field_bonus: 0.05,
  rich_metadata_bonus_cap: 0.15,
  f1_min_score: 0.35,
  series_name_match_boost: 1.4,
  series_number_exact_boost: 2.0,
  series_number_wrong_penalty: 0.5,
  bulk_fetch_workers: 4,
  duration_tier_multipliers: [1.3, 1.2, 1.1, 1.0, 0.75, 0.5],
  duration_tier_scores: [20, 15, 10, 0, -10, -20],
} satisfies Partial<api.MetadataScoringConfig>;

// numPatch parses a numeric input value into a scoring patch value. An
// empty/blank input becomes `undefined` so the key is ABSENT in the saved
// payload (JSON.stringify drops undefined) and the backend fail-open default
// applies — never NaN. An explicit "0" stays 0, which the pointer knobs
// (compilation_penalty, rich_metadata_bonus_cap, f1_min_score) honor as a real
// operator value.
function numPatch(value: string): number | undefined {
  return value.trim() === '' ? undefined : Number(value);
}

export function MetadataScoringSection({ config, onChange }: MetadataScoringProps) {
  // Numeric field bound to a single scoring key. Displays '' when unset so an
  // empty input round-trips as absent rather than NaN.
  const numField = (
    key: keyof api.MetadataScoringConfig,
    label: string,
    helperText: string,
    step = 0.05,
    min?: number,
  ) => (
    <Grid item xs={12} sm={6} key={key}>
      <TextField
        fullWidth
        type="number"
        label={label}
        value={(config[key] as number | undefined) ?? ''}
        onChange={(e) =>
          onChange({ [key]: numPatch(e.target.value) } as Partial<api.MetadataScoringConfig>)
        }
        helperText={helperText}
        size="small"
        inputProps={min !== undefined ? { min, step } : { step }}
      />
    </Grid>
  );

  const resetButton = (label: string, patch: Partial<api.MetadataScoringConfig>) => (
    <Grid item xs={12}>
      <Button size="small" variant="outlined" onClick={() => onChange(patch)}>
        {label}
      </Button>
    </Grid>
  );

  // Duration tier VALUE array (multipliers or scores). Each element is a numeric
  // input; editing one rebuilds the array so the whole array round-trips. Tier
  // EDGES are fixed in code and are NOT rendered as inputs.
  const durationArray = (
    key: 'duration_tier_multipliers' | 'duration_tier_scores',
    label: string,
    step: number,
  ) => {
    const values = config[key] ?? SCORING_DEFAULTS[key];
    return (
      <Grid item xs={12}>
        <Typography variant="body2" gutterBottom>
          {label}
        </Typography>
        <Grid container spacing={1}>
          {values.map((v, i) => (
            <Grid item xs={4} sm={2} key={`${key}-${i}`}>
              <TextField
                fullWidth
                type="number"
                label={`Tier ${i + 1}`}
                value={v}
                onChange={(e) => {
                  const next = [...values];
                  next[i] = Number(e.target.value);
                  onChange({ [key]: next } as Partial<api.MetadataScoringConfig>);
                }}
                size="small"
                inputProps={{ step }}
              />
            </Grid>
          ))}
        </Grid>
      </Grid>
    );
  };

  return (
    <Box>
      <Typography variant="h6" gutterBottom>
        Metadata Scoring
      </Typography>

      <Grid container spacing={2}>
        <Grid item xs={12} sm={6}>
          <FormControlLabel
            control={
              <Switch
                checked={config.embedding_enabled}
                onChange={(e) => onChange({ embedding_enabled: e.target.checked })}
              />
            }
            label="Use embedding similarity in metadata scoring"
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            fullWidth
            type="number"
            label="Embedding min score"
            value={config.embedding_min_score}
            onChange={(e) => onChange({ embedding_min_score: Number(e.target.value) })}
            helperText="Minimum embedding score (0–1)"
            size="small"
            inputProps={{ min: 0, max: 1, step: 0.01 }}
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            fullWidth
            type="number"
            label="Embedding best match threshold"
            value={config.embedding_best_match}
            onChange={(e) => onChange({ embedding_best_match: Number(e.target.value) })}
            helperText="Best-match threshold (0–1)"
            size="small"
            inputProps={{ min: 0, max: 1, step: 0.01 }}
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <FormControlLabel
            control={
              <Switch
                checked={config.llm_enabled}
                onChange={(e) => onChange({ llm_enabled: e.target.checked })}
              />
            }
            label="Use LLM to rerank top candidates"
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            fullWidth
            type="number"
            label="LLM rerank epsilon"
            value={config.llm_rerank_epsilon}
            onChange={(e) => onChange({ llm_rerank_epsilon: Number(e.target.value) })}
            helperText="Tie-break tolerance"
            size="small"
            inputProps={{ min: 0, step: 0.01 }}
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            fullWidth
            type="number"
            label="LLM rerank top K"
            value={config.llm_rerank_top_k}
            onChange={(e) => onChange({ llm_rerank_top_k: Number(e.target.value) })}
            helperText="Number of candidates sent to LLM"
            size="small"
            inputProps={{ min: 1 }}
          />
        </Grid>

        <Grid item xs={12}>
          <FormControlLabel
            control={
              <Switch
                checked={config.write_backup_before}
                onChange={(e) => onChange({ write_backup_before: e.target.checked })}
              />
            }
            label="Write tag backup before applying metadata"
          />
        </Grid>
      </Grid>

      <Divider sx={{ my: 3 }} />

      {/* Transcription boosts */}
      <Typography variant="subtitle1" gutterBottom>
        Transcription boosts
      </Typography>
      <Grid container spacing={2}>
        {numField(
          'transcription_title_exact_boost',
          'Title exact-match boost',
          'Multiplier when the transcription title matches exactly',
        )}
        {numField(
          'transcription_title_substr_boost',
          'Title substring boost',
          'Multiplier when the transcription title matches as a substring',
        )}
        {numField(
          'transcription_author_boost',
          'Author boost',
          'Multiplier when the transcription author matches',
        )}
        {numField(
          'transcription_narrator_boost',
          'Narrator boost',
          'Multiplier when the transcription narrator matches',
        )}
        {resetButton('Reset transcription boosts to defaults', {
          transcription_title_exact_boost: SCORING_DEFAULTS.transcription_title_exact_boost,
          transcription_title_substr_boost: SCORING_DEFAULTS.transcription_title_substr_boost,
          transcription_author_boost: SCORING_DEFAULTS.transcription_author_boost,
          transcription_narrator_boost: SCORING_DEFAULTS.transcription_narrator_boost,
        })}
      </Grid>

      <Divider sx={{ my: 3 }} />

      {/* Score adjustments */}
      <Typography variant="subtitle1" gutterBottom>
        Score adjustments
      </Typography>
      <Grid container spacing={2}>
        {numField(
          'compilation_penalty',
          'Compilation penalty',
          'Penalty for compilation candidates (0 is a real value; leave empty for default)',
        )}
        {numField(
          'rich_metadata_field_bonus',
          'Rich-metadata field bonus',
          'Per-field bonus for candidates with rich metadata',
        )}
        {numField(
          'rich_metadata_bonus_cap',
          'Rich-metadata bonus cap',
          'Cap on total rich-metadata bonus (0 is a real value; leave empty for default)',
        )}
        {numField(
          'f1_min_score',
          'F1 minimum score',
          'Minimum score floor (0 is a real value; leave empty for default)',
        )}
        {resetButton('Reset score adjustments to defaults', {
          compilation_penalty: SCORING_DEFAULTS.compilation_penalty,
          rich_metadata_field_bonus: SCORING_DEFAULTS.rich_metadata_field_bonus,
          rich_metadata_bonus_cap: SCORING_DEFAULTS.rich_metadata_bonus_cap,
          f1_min_score: SCORING_DEFAULTS.f1_min_score,
        })}
      </Grid>

      <Divider sx={{ my: 3 }} />

      {/* Series boosts */}
      <Typography variant="subtitle1" gutterBottom>
        Series boosts
      </Typography>
      <Grid container spacing={2}>
        {numField(
          'series_name_match_boost',
          'Series name match boost',
          'Multiplier when the series name matches',
        )}
        {numField(
          'series_number_exact_boost',
          'Series number exact boost',
          'Multiplier when the series number matches exactly',
        )}
        {numField(
          'series_number_wrong_penalty',
          'Series number wrong penalty',
          'Penalty when the series number is wrong',
        )}
        {resetButton('Reset series boosts to defaults', {
          series_name_match_boost: SCORING_DEFAULTS.series_name_match_boost,
          series_number_exact_boost: SCORING_DEFAULTS.series_number_exact_boost,
          series_number_wrong_penalty: SCORING_DEFAULTS.series_number_wrong_penalty,
        })}
      </Grid>

      <Divider sx={{ my: 3 }} />

      {/* Duration tiers */}
      <Typography variant="subtitle1" gutterBottom>
        Duration tier values
      </Typography>
      <Typography variant="body2" color="text.secondary" gutterBottom>
        Tier edges are fixed in code; only the multiplier and score values are editable.
      </Typography>
      <Grid container spacing={2}>
        {durationArray('duration_tier_multipliers', 'Multipliers', 0.05)}
        {durationArray('duration_tier_scores', 'Scores', 1)}
        {resetButton('Reset duration tiers to defaults', {
          duration_tier_multipliers: [...SCORING_DEFAULTS.duration_tier_multipliers],
          duration_tier_scores: [...SCORING_DEFAULTS.duration_tier_scores],
        })}
      </Grid>

      <Divider sx={{ my: 3 }} />

      {/* Bulk fetch */}
      <Typography variant="subtitle1" gutterBottom>
        Bulk fetch
      </Typography>
      <Grid container spacing={2}>
        {numField(
          'bulk_fetch_workers',
          'Bulk fetch workers',
          'Concurrent workers for bulk metadata fetch',
          1,
          1,
        )}
        {resetButton('Reset bulk fetch to defaults', {
          bulk_fetch_workers: SCORING_DEFAULTS.bulk_fetch_workers,
        })}
      </Grid>
    </Box>
  );
}
