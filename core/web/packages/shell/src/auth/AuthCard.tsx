// file: core/web/packages/shell/src/auth/AuthCard.tsx
// version: 0.1.0
// guid: b6ea3633-cea4-414c-861b-f07dc8712ad3
// last-edited: 2026-11-20

import { Box, Paper, Typography } from '@mui/material';
import type { ReactNode } from 'react';

/** The centred card the public auth pages share. */
export function AuthCard({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Box sx={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', p: 2 }}>
      <Paper component="main" sx={{ p: 4, width: '100%', maxWidth: 420 }}>
        <Typography variant="h5" component="h1" gutterBottom>
          {title}
        </Typography>
        {children}
      </Paper>
    </Box>
  );
}
