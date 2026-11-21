// file: web/src/components/diagnostics/DiagnosticsFrame.tsx
// version: 0.1.0
// guid: 8917d928-20a3-4bea-922b-93f3f642833d
// last-edited: 2026-11-21

// F9-04 slice 3: the shell half of the Diagnostics page (07 §7.11: the `/system/diagnostics`
// frame): the page heading and a slot for the modules' diagnostics widgets. It imports no books
// code and moves to core/web/packages/shell/src/pages/ with the other shell pages.

import type { ReactNode } from 'react';
import { Box, Typography } from '@mui/material';

export function DiagnosticsFrame({ children }: { children?: ReactNode }) {
  return (
    <Box sx={{ p: 3 }}>
      <Typography variant="h4" gutterBottom>
        Diagnostics
      </Typography>

      {children}
    </Box>
  );
}
