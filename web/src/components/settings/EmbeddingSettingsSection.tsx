// file: web/src/components/settings/EmbeddingSettingsSection.tsx
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-07-01

import {
  Box,
  Typography,
  TextField,
  FormControlLabel,
  Switch,
  MenuItem,
  Grid,
  Link,
} from '@mui/material';
import * as api from '../../services/api';

interface EmbeddingSettingsSectionProps {
  config: api.EmbeddingConfig;
  onChange: (patch: Partial<api.EmbeddingConfig>) => void;
}

export function EmbeddingSettingsSection({ config, onChange }: EmbeddingSettingsSectionProps) {
  return (
    <Box>
      <Typography variant="h6" gutterBottom>
        Embedding Settings
      </Typography>

      <Box mb={2}>
        <Link href="https://ollama.com/download" target="_blank" rel="noopener noreferrer">
          Download the latest Ollama
        </Link>
      </Box>

      <Grid container spacing={2}>
        <Grid item xs={12}>
          <FormControlLabel
            control={
              <Switch
                checked={config.enabled}
                onChange={(e) => onChange({ enabled: e.target.checked })}
              />
            }
            label="Enable embedding generation"
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            fullWidth
            label="Model"
            value={config.model}
            onChange={(e) => onChange({ model: e.target.value })}
            helperText="e.g. bge-m3, text-embedding-3-large"
            size="small"
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            fullWidth
            type="number"
            label="Dimensions"
            value={config.dimensions}
            onChange={(e) => onChange({ dimensions: Number(e.target.value) })}
            helperText="Vector dimensions"
            size="small"
            inputProps={{ min: 1 }}
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            fullWidth
            label="Base URL"
            value={config.base_url}
            onChange={(e) => onChange({ base_url: e.target.value })}
            helperText="Blank = OpenAI. Set to http://localhost:11434/v1 for Ollama."
            size="small"
          />
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            select
            fullWidth
            label="Vector backend"
            value={config.vector_backend}
            onChange={(e) => onChange({ vector_backend: e.target.value })}
            size="small"
          >
            <MenuItem value="hnsw">HNSW</MenuItem>
            <MenuItem value="chromem">Chromem</MenuItem>
          </TextField>
        </Grid>
      </Grid>
    </Box>
  );
}
