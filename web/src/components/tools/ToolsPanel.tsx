// file: web/src/components/tools/ToolsPanel.tsx
// version: 1.0.0
// guid: f8a9b0c1-d2e3-4567-fabc-567890123456
// last-edited: 2026-06-15

import { useState, useEffect } from 'react';
import {
  Box, Card, CardContent, Typography, Chip, Button,
  CircularProgress, Tooltip, Stack,
} from '@mui/material';
import { getTools, installTool, ToolStatus } from '../../services/api';

interface ToolsPanelProps {
  mode: 'wizard' | 'settings';
}

const TOOL_TOOLTIPS: Record<string, string> = {
  ollama:  'Powers local AI deduplication — no data leaves your machine. Uses ~5GB RAM while active; stops automatically when idle. ~5GB download.',
  fpcalc: 'Enables audio fingerprint matching to identify duplicate recordings. ~2MB download.',
};

export function ToolsPanel({ mode }: ToolsPanelProps) {
  const [tools, setTools] = useState<ToolStatus[]>([]);
  const [installing, setInstalling] = useState<Record<string, boolean>>({});
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    getTools().then(setTools).catch((e: Error) => setError(e.message));
  }, []);

  const handleInstall = async (name: string) => {
    setInstalling(prev => ({ ...prev, [name]: true }));
    try {
      await installTool(name);
      const updated = await getTools();
      setTools(updated);
    } catch (e: unknown) {
      setError((e as Error).message);
    } finally {
      setInstalling(prev => ({ ...prev, [name]: false }));
    }
  };

  if (error) return <Typography color="error">{error}</Typography>;

  return (
    <Stack spacing={2}>
      {tools.map(tool => (
        <Card key={tool.name} variant="outlined">
          <CardContent>
            <Stack direction="row" alignItems="center" spacing={1} mb={1}>
              <Tooltip title={TOOL_TOOLTIPS[tool.name] ?? ''}>
                <Typography variant="subtitle1" fontWeight="bold">
                  {tool.name}
                </Typography>
              </Tooltip>
              <Chip
                size="small"
                label={tool.available ? 'Available' : 'Not available'}
                color={tool.available ? 'success' : 'default'}
              />
              {tool.version && (
                <Chip size="small" label={`v${tool.version}`} variant="outlined" />
              )}
            </Stack>

            {tool.resolved_path && (
              <Typography variant="caption" color="text.secondary" display="block">
                {tool.resolved_path}
              </Typography>
            )}

            {!tool.available && tool.mode !== 'disabled' && (
              <Button
                size="small"
                variant="contained"
                onClick={() => handleInstall(tool.name)}
                disabled={installing[tool.name]}
                startIcon={installing[tool.name] ? <CircularProgress size={14} /> : undefined}
                sx={{ mt: 1 }}
              >
                {installing[tool.name] ? 'Installing…' : 'Install'}
              </Button>
            )}
          </CardContent>
        </Card>
      ))}
    </Stack>
  );
}
