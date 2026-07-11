// file: web/src/components/audiobooks/LoadingWithCancel.tsx
// version: 1.0.0
// guid: 2f8c4a1d-6b3e-4d7a-9c5f-8e1b3a6d2f4c
// last-edited: 2026-07-11

import { useEffect, useState } from 'react';
import { Box, CircularProgress, Button, Typography } from '@mui/material';

const SLOW_THRESHOLD_MS = 3000;

interface LoadingWithCancelProps {
  /** Omit to show a plain indefinite spinner with no cancel affordance. */
  onCancel?: () => void;
}

// Remounts (and so resets its own elapsed timer) whenever the parent's
// `loading` branch flips false -> true, since the parent conditionally
// renders this component only while loading is true.
export function LoadingWithCancel({ onCancel }: LoadingWithCancelProps) {
  const [slow, setSlow] = useState(false);

  useEffect(() => {
    const timer = window.setTimeout(() => setSlow(true), SLOW_THRESHOLD_MS);
    return () => window.clearTimeout(timer);
  }, []);

  return (
    <Box
      display="flex"
      flexDirection="column"
      justifyContent="center"
      alignItems="center"
      minHeight="400px"
      gap={2}
    >
      <CircularProgress />
      {slow && onCancel && (
        <>
          <Typography variant="body2" color="text.secondary">
            Still loading&hellip;
          </Typography>
          <Button variant="outlined" size="small" onClick={onCancel}>
            Cancel
          </Button>
        </>
      )}
    </Box>
  );
}
