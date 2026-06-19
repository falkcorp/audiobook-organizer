// file: web/src/components/settings/MetadataScoringSection.tsx
// version: 1.0.0
// guid: c3d4e5f6-a7b8-9012-cdef-123456789012
// last-edited: 2026-06-19

import {
  Box,
  Typography,
  TextField,
  FormControlLabel,
  Switch,
  Grid,
} from '@mui/material';
import * as api from '../../services/api';

interface MetadataScoringProps {
  config: api.MetadataScoringConfig;
  onChange: (patch: Partial<api.MetadataScoringConfig>) => void;
}

export function MetadataScoringSection({ config, onChange }: MetadataScoringProps) {
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
    </Box>
  );
}
