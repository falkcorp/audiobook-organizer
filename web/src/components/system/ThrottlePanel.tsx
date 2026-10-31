// file: web/src/components/system/ThrottlePanel.tsx
// version: 1.0.0
// guid: 7d2f4c1e-3b9a-4e68-a5c0-91f6d8b2e473
// last-edited: 2026-10-30

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  CardHeader,
  Chip,
  CircularProgress,
  Stack,
  Typography,
} from '@mui/material';
import RefreshIcon from '@mui/icons-material/Refresh';
import * as api from '../../services/api';
import { useHasPermission } from '../../hooks/useLibraryPermission';

/** The server route behind op starts (POST /operations/v2) asks for this. */
export const THROTTLE_RESET_PERMISSION = 'scan.trigger';

/** Holds last minutes to hours, so a slow poll is enough (the ask was ~4h). */
export const THROTTLE_POLL_MS = 4 * 60 * 60 * 1000;

/** The scope of the op that is running: one provider id, or every hold. */
const ALL = '\u0000all';

function formatRemaining(seconds: number): string {
  if (seconds <= 0) return 'expiring';
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (h > 0) return `${h}h ${m}m left`;
  if (m > 0) return `${m}m left`;
  return `${seconds}s left`;
}

/**
 * Lists the metadata providers currently on hold (quota, auth or rate limit) and
 * releases them by starting maintenance.throttle-reset with dry_run=false. The
 * op shows up in the operations bell like every other UI-started op; the
 * buttons stay disabled until it settles, then the list is refetched.
 */
export function ThrottlePanel() {
  const mayReset = useHasPermission(THROTTLE_RESET_PERMISSION);
  const [holds, setHolds] = useState<api.ProviderThrottle[]>([]);
  const [loading, setLoading] = useState(true);
  const [resetting, setResetting] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const mounted = useRef(true);

  const load = useCallback(async () => {
    try {
      const list = await api.getProviderThrottles();
      if (mounted.current) setHolds(list);
    } catch (e) {
      if (mounted.current) setError(e instanceof Error ? e.message : 'Failed to load throttles');
    } finally {
      if (mounted.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    void load();
    const timer = setInterval(() => void load(), THROTTLE_POLL_MS);
    return () => {
      mounted.current = false;
      clearInterval(timer);
    };
  }, [load]);

  const reset = async (provider?: string) => {
    setResetting(provider ?? ALL);
    setError(null);
    try {
      const { operation_id: opId } = await api.startThrottleReset(provider);
      if (opId) {
        const op = await api.pollOperationV2(opId);
        if (op.status !== 'completed' && mounted.current) {
          setError(op.error_message || `Throttle reset ended ${op.status}`);
        }
      }
      await load();
    } catch (e) {
      if (mounted.current) setError(e instanceof Error ? e.message : 'Failed to reset throttle');
    } finally {
      if (mounted.current) setResetting(null);
    }
  };

  const busy = resetting !== null;

  return (
    <Card sx={{ mb: 3 }} data-testid="throttle-panel">
      <CardHeader
        title="Provider Throttles"
        subheader="Metadata providers on hold after a quota, auth or rate-limit failure. A hold ends on expiry or on the next successful call; Reset now releases it early."
        action={
          <Stack direction="row" spacing={1}>
            <Button
              size="small"
              startIcon={<RefreshIcon />}
              disabled={loading || busy}
              onClick={() => void load()}
            >
              Refresh
            </Button>
            <Button
              size="small"
              variant="outlined"
              color="warning"
              disabled={!mayReset || busy || holds.length === 0}
              onClick={() => void reset()}
            >
              Reset all
            </Button>
          </Stack>
        }
      />
      <CardContent>
        {error && (
          <Alert severity="error" sx={{ mb: 1.5 }} onClose={() => setError(null)}>
            {error}
          </Alert>
        )}
        {loading ? (
          <CircularProgress size={24} />
        ) : holds.length === 0 ? (
          <Typography variant="body2" sx={{ color: 'text.secondary' }}>
            No providers are on hold.
          </Typography>
        ) : (
          <Stack spacing={1}>
            {holds.map((h) => (
              <Box
                key={h.provider_id}
                data-testid={`throttle-${h.provider_id}`}
                sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap' }}
              >
                <Box sx={{ flex: 1 }}>
                  <Typography variant="body2" sx={{ fontWeight: 'medium' }}>
                    {h.provider_id}
                    <Chip size="small" label={h.reason} sx={{ ml: 1 }} />
                    <Chip
                      size="small"
                      variant="outlined"
                      label={formatRemaining(h.seconds_remaining)}
                      sx={{ ml: 1 }}
                    />
                  </Typography>
                  {h.detail && (
                    <Typography variant="caption" sx={{ color: 'text.secondary' }}>
                      {h.detail}
                    </Typography>
                  )}
                </Box>
                <Button
                  size="small"
                  variant="outlined"
                  disabled={!mayReset || busy}
                  onClick={() => void reset(h.provider_id)}
                >
                  {resetting === h.provider_id ? 'Resetting…' : 'Reset now'}
                </Button>
              </Box>
            ))}
          </Stack>
        )}
      </CardContent>
    </Card>
  );
}
