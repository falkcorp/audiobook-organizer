// file: web/src/components/dashboard/DashboardFrame.tsx
// version: 0.1.0
// guid: 80ce7019-3047-4ec9-be3b-526df46d2a39
// last-edited: 2026-11-21

// F9-04 slice 2: the shell half of the Dashboard (07 §7.11: `pages/Dashboard.tsx` -> the shell's
// `/` home): the page heading, the announcement banner and a slot for the module widgets. It
// imports no books code and moves to core/web/packages/shell/src/pages/ once F9-05's
// AnnouncementBanner is in the shell.

import type { ReactNode } from 'react';
import { Box, Typography } from '@mui/material';
import { AnnouncementBanner } from '../AnnouncementBanner';

export function DashboardFrame({ children }: { children?: ReactNode }) {
  return (
    <Box sx={{ height: '100%', overflow: 'auto' }}>
      <Typography variant="h4" gutterBottom>
        Dashboard
      </Typography>

      <AnnouncementBanner />

      {children}
    </Box>
  );
}
