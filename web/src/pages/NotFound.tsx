// file: web/src/pages/NotFound.tsx
// version: 1.0.0
// guid: 3b9d6f12-8c4e-4e07-a1d5-5f2c7b8e0a96
// last-edited: 2026-10-19

import { Box, Button, Stack, Typography } from '@mui/material';
import { Link as RouterLink, useLocation } from 'react-router-dom';

/** Catch-all for signed-in routes: says what was requested and where to go next. */
export function NotFound() {
  const { pathname, search, hash } = useLocation();
  return (
    <Box sx={{ p: 4 }} data-testid="not-found">
      <Stack spacing={2} sx={{ alignItems: 'flex-start' }}>
        <Typography variant="h4" component="h1">
          Page not found
        </Typography>
        <Typography variant="body1">
          There is no page at{' '}
          <Box component="code" data-testid="not-found-path">
            {pathname + search + hash}
          </Box>
          .
        </Typography>
        <Stack direction="row" spacing={2}>
          <Button component={RouterLink} to="/dashboard" variant="contained">
            Go to Dashboard
          </Button>
          <Button component={RouterLink} to="/settings" variant="outlined">
            Go to Settings
          </Button>
        </Stack>
      </Stack>
    </Box>
  );
}
