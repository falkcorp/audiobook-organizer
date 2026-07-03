// file: web/src/components/settings/AIBackendsSection.tsx
// version: 1.0.0
// guid: 9e1f2a3b-4c5d-6e7f-8a9b-0c1d2e3f4a5b
// last-edited: 2026-07-03

import { useState } from 'react';
import {
  Box,
  Typography,
  TextField,
  MenuItem,
  Grid,
  Button,
  Alert,
  Chip,
  Stack,
  CircularProgress,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogContentText,
  DialogActions,
} from '@mui/material';
import * as api from '../../services/api';

interface AIBackendsSectionProps {
  config: api.AIBackendConfig;
  onChange: (patch: Partial<api.AIBackendConfig>) => void;
}

const MODE_OPTIONS: { value: api.AIBackendMode; label: string }[] = [
  { value: 'disabled', label: 'Disabled' },
  { value: 'openai', label: 'OpenAI' },
  { value: 'local', label: 'Local (Ollama)' },
  { value: 'openai-fallback-local', label: 'OpenAI (fallback to local)' },
];

function usesLocal(mode: string): boolean {
  return mode === 'local' || mode === 'openai-fallback-local';
}

export function AIBackendsSection({ config, onChange }: AIBackendsSectionProps) {
  const [status, setStatus] = useState<api.AIBackendsStatus | null>(null);
  const [testing, setTesting] = useState(false);
  const [testError, setTestError] = useState<string | null>(null);
  const [pullTarget, setPullTarget] = useState<{ model: string; label: string } | null>(null);
  const [pulling, setPulling] = useState(false);
  const [pullError, setPullError] = useState<string | null>(null);

  const showLocalFields = usesLocal(config.embedding_mode) || usesLocal(config.llm_mode);

  const runStatusCheck = async () => {
    setTesting(true);
    setTestError(null);
    try {
      const result = await api.getAIBackendsStatus();
      setStatus(result);
    } catch (e: unknown) {
      setTestError((e as Error).message);
    } finally {
      setTesting(false);
    }
  };

  const handlePullConfirm = async () => {
    if (!pullTarget) return;
    setPulling(true);
    setPullError(null);
    try {
      await api.pullAIBackendModel(pullTarget.model);
      setPullTarget(null);
      const result = await api.getAIBackendsStatus();
      setStatus(result);
    } catch (e: unknown) {
      setPullError((e as Error).message);
    } finally {
      setPulling(false);
    }
  };

  const absentModels = status
    ? [status.embedding_model, status.llm_model].filter(
        (m): m is api.AIBackendModelStatus => !!m && !m.pulled
      )
    : [];

  return (
    <Box>
      <Typography variant="h6" gutterBottom>
        AI Backends
      </Typography>
      <Typography variant="body2" color="text.secondary" mb={2}>
        Choose which backend serves embeddings and LLM/chat requests, independently.
      </Typography>

      <Grid container spacing={2}>
        <Grid item xs={12} sm={6}>
          <TextField
            select
            fullWidth
            label="Embedding mode"
            value={config.embedding_mode || 'disabled'}
            onChange={(e) => onChange({ embedding_mode: e.target.value })}
            size="small"
          >
            {MODE_OPTIONS.map((opt) => (
              <MenuItem key={opt.value} value={opt.value}>
                {opt.label}
              </MenuItem>
            ))}
          </TextField>
        </Grid>

        <Grid item xs={12} sm={6}>
          <TextField
            select
            fullWidth
            label="LLM mode"
            value={config.llm_mode || 'disabled'}
            onChange={(e) => onChange({ llm_mode: e.target.value })}
            size="small"
          >
            {MODE_OPTIONS.map((opt) => (
              <MenuItem key={opt.value} value={opt.value}>
                {opt.label}
              </MenuItem>
            ))}
          </TextField>
        </Grid>

        {showLocalFields && (
          <>
            <Grid item xs={12} sm={4}>
              <TextField
                fullWidth
                label="Local base URL"
                value={config.local_base_url}
                onChange={(e) => onChange({ local_base_url: e.target.value })}
                helperText="e.g. http://192.168.0.20:11434/v1"
                size="small"
              />
            </Grid>
            <Grid item xs={12} sm={4}>
              <TextField
                fullWidth
                label="Local embedding model"
                value={config.local_embedding_model}
                onChange={(e) => onChange({ local_embedding_model: e.target.value })}
                helperText="e.g. bge-m3"
                size="small"
              />
            </Grid>
            <Grid item xs={12} sm={4}>
              <TextField
                fullWidth
                label="Local LLM model"
                value={config.local_llm_model}
                onChange={(e) => onChange({ local_llm_model: e.target.value })}
                helperText="e.g. qwen2.5:7b-instruct"
                size="small"
              />
            </Grid>
          </>
        )}

        <Grid item xs={12}>
          <Button
            variant="outlined"
            size="small"
            onClick={runStatusCheck}
            disabled={testing}
            startIcon={testing ? <CircularProgress size={14} /> : undefined}
          >
            {testing ? 'Testing…' : 'Test Connection'}
          </Button>
        </Grid>

        {testError && (
          <Grid item xs={12}>
            <Alert severity="error">{testError}</Alert>
          </Grid>
        )}

        {status && (
          <Grid item xs={12}>
            <Stack spacing={1}>
              <Stack direction="row" spacing={1} alignItems="center" flexWrap="wrap">
                <Chip size="small" label={`Embedding: ${status.embedding_mode}`} />
                <Chip size="small" label={`LLM: ${status.llm_mode}`} />
                <Chip
                  size="small"
                  label={status.local_reachable ? 'Local endpoint reachable' : 'Local endpoint unreachable'}
                  color={status.local_reachable ? 'success' : 'default'}
                />
              </Stack>
              {status.fallback_reason && (
                <Alert severity="warning">{status.fallback_reason}</Alert>
              )}
              {[status.embedding_model, status.llm_model]
                .filter((m): m is api.AIBackendModelStatus => !!m)
                .map((m) => (
                  <Stack key={m.name} direction="row" spacing={1} alignItems="center">
                    <Typography variant="body2">{m.name}</Typography>
                    <Chip
                      size="small"
                      label={m.pulled ? 'Pulled' : 'Not pulled'}
                      color={m.pulled ? 'success' : 'warning'}
                    />
                    {!m.pulled && (
                      <Button
                        size="small"
                        onClick={() => setPullTarget({ model: m.name, label: m.name })}
                      >
                        Pull now
                      </Button>
                    )}
                  </Stack>
                ))}
              {absentModels.length === 0 && status.local_reachable && (
                <Alert severity="success">All configured local models are pulled.</Alert>
              )}
            </Stack>
          </Grid>
        )}
      </Grid>

      <Dialog open={!!pullTarget} onClose={() => (pulling ? undefined : setPullTarget(null))}>
        <DialogTitle>Pull model?</DialogTitle>
        <DialogContent>
          <DialogContentText>
            {pullTarget ? `${pullTarget.label} not pulled — Pull now?` : ''}
          </DialogContentText>
          {pullError && (
            <Alert severity="error" sx={{ mt: 2 }}>
              {pullError}
            </Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setPullTarget(null)} disabled={pulling}>
            Cancel
          </Button>
          <Button
            onClick={handlePullConfirm}
            variant="contained"
            disabled={pulling}
            startIcon={pulling ? <CircularProgress size={14} /> : undefined}
          >
            {pulling ? 'Installing…' : 'Confirm'}
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
