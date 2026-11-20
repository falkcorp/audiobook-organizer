// file: core/web/packages/shell/src/app/Page.tsx
// version: 0.1.0
// guid: 6b1e8d24-3f57-4a90-8c12-d9e4a7b0f365
// last-edited: 2026-11-20

// The page frame and the two fallback pages the route table renders
// eagerly; the shell's real pages are lazy chunks (shellPages.tsx).

import { useEffect, type ReactNode } from 'react';
import { Box, Typography } from '@mui/material';
import { Link as RouterLink } from 'react-router';

export function Page({ title, children }: { title: string; children: ReactNode }) {
  useEffect(() => {
    document.title = title;
  }, [title]);
  return (
    <Box sx={{ p: 3 }}>
      <Typography variant="h4" component="h1" gutterBottom>
        {title}
      </Typography>
      {children}
    </Box>
  );
}

export function NotFoundPage() {
  return (
    <Page title="Not found">
      <Typography>
        Nothing lives at this address. <RouterLink to="/">Go home</RouterLink>
      </Typography>
    </Page>
  );
}

export function NoAccessPage() {
  return (
    <Page title="No access">
      <Typography>Your account does not have the permission this page needs.</Typography>
    </Page>
  );
}
