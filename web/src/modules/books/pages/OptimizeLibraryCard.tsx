// file: web/src/modules/books/pages/OptimizeLibraryCard.tsx
// version: 0.1.0
// guid: 9671c3ae-cbb5-4227-a1ea-da0abfd42015
// last-edited: 2026-11-21

// Books half of the Maintenance tab: the Optimize Library card (F9-04 slice 1).

import { useState } from 'react';
import {
  Alert,
  Box,
  Button,
  Card,
  Typography,
} from '@mui/material';
import * as api from '../../../services/api';

export function OptimizeLibraryCard() {
  const [optimizeRunning, setOptimizeRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [successMsg, setSuccessMsg] = useState<string | null>(null);
  const handleOptimize = async () => {
    setOptimizeRunning(true);
    setError(null);
    setSuccessMsg(null);
    try {
      const result = await api.startOptimize();
      setSuccessMsg(`Optimize started (op ${result.operation_id})`);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to start optimize');
    } finally {
      setOptimizeRunning(false);
    }
  };

  return (
    <Card sx={{ mb: 3, p: 2, borderLeft: '4px solid', borderColor: 'primary.main' }}>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <Box>
          <Typography variant="h6">⚡ Optimize Library</Typography>
          <Typography variant="body2" color="textSecondary">
            Run cleanup, fingerprint missing, dedup scan, and ID backfill in order.
          </Typography>
        </Box>
        <Button
          variant="contained"
          color="primary"
          onClick={handleOptimize}
          disabled={optimizeRunning}
        >
          {optimizeRunning ? 'Running...' : 'Run Optimize'}
        </Button>
      </Box>
      {error && (
        <Alert severity="error" sx={{ mt: 2 }} onClose={() => setError(null)}>
          {error}
        </Alert>
      )}
      {successMsg && (
        <Alert severity="success" sx={{ mt: 2 }} onClose={() => setSuccessMsg(null)}>
          {successMsg}
        </Alert>
      )}
    </Card>
  );
}
