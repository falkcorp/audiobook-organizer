// file: web/src/components/settings/ToolsSettingsTab.tsx
// version: 1.0.0
// guid: a9b0c1d2-e3f4-5678-abcd-678901234567
// last-edited: 2026-06-15

import { Box, Typography, Switch, FormControlLabel, TextField, Divider, Button } from '@mui/material';
import { ToolsPanel } from '../tools/ToolsPanel';
import { useAdvancedSettings } from '../../hooks/useAdvancedSettings';

export function ToolsSettingsTab() {
  const { showAdvanced, toggleAdvanced } = useAdvancedSettings();

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={2}>
        <Typography variant="h6">External Tools</Typography>
        <Button size="small" variant="outlined" onClick={toggleAdvanced}>
          {showAdvanced ? 'Hide Advanced' : 'Show Advanced'}
        </Button>
      </Box>

      <ToolsPanel mode="settings" />

      <Divider sx={{ my: 3 }} />

      <Typography variant="subtitle1" gutterBottom>Ollama Duty Cycle</Typography>
      <FormControlLabel
        control={<Switch />}
        label="Allow periodic Ollama (spin up when new books need embedding)"
      />
      {showAdvanced && (
        <TextField
          size="small"
          label="Debounce interval (minutes)"
          type="number"
          defaultValue={10}
          sx={{ mt: 1, width: 200 }}
          helperText="How long to wait after the last new book before starting an embed batch."
        />
      )}

      {showAdvanced && (
        <>
          <Divider sx={{ my: 2 }} />
          <Typography variant="subtitle2" color="text.secondary" gutterBottom>
            Advanced
          </Typography>
          <TextField
            size="small"
            label="Managed tools directory"
            defaultValue="/var/lib/audiobook-organizer/tools"
            fullWidth
            helperText="Where auto-downloaded binaries are stored."
          />
        </>
      )}
    </Box>
  );
}
