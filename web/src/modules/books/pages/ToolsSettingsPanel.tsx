// file: web/src/modules/books/pages/ToolsSettingsPanel.tsx
// version: 0.1.0
// guid: 360965a1-d4c1-4f60-b511-b5831158c389
// last-edited: 2026-11-21

// The Tools tab.

import {
  Typography,
  TextField,
  Accordion,
  AccordionSummary,
  AccordionDetails,
} from '@mui/material';
import { ToolsSettingsTab } from '../../../components/settings/ToolsSettingsTab';
import { ExpandMore as ExpandMoreIcon } from '@mui/icons-material';
import { useBooksSettings } from './booksSettingsContext';

export function ToolsSettingsPanel() {
  const { handleToolsChange, toolsConfig } = useBooksSettings();

  return (
    <>
      <ToolsSettingsTab />
      <Accordion sx={{ mt: 3 }}>
        <AccordionSummary expandIcon={<ExpandMoreIcon />}>
          <Typography>Advanced: Tools Config</Typography>
        </AccordionSummary>
        <AccordionDetails>
          <TextField
            label="Managed tools directory"
            value={toolsConfig.managed_dir}
            onChange={(e) => handleToolsChange({ managed_dir: e.target.value })}
            fullWidth
            helperText="Directory where managed binaries (Ollama, fpcalc) are downloaded"
          />
        </AccordionDetails>
      </Accordion>
    </>
  );
}
