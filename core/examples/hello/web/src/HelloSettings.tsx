// file: core/examples/hello/web/src/HelloSettings.tsx
// version: 0.1.0
// guid: 9a4c1e85-3f72-4d60-b0e8-5c2d7a9f1b36
// last-edited: 2026-11-20

import { Box, Typography } from '@mui/material';

/**
 * hello's settings panel. hello declares no settings keys yet
 * (module.Manifest.Settings has no owning row), so the panel says so instead
 * of rendering a schema form over an empty section.
 */
export default function HelloSettings() {
  return (
    <Box sx={{ p: 2 }}>
      <Typography>hello has no settings yet.</Typography>
    </Box>
  );
}
