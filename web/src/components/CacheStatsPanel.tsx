// file: web/src/components/CacheStatsPanel.tsx
// version: 1.1.2
// guid: b5c8d9ea-1f2g-3h4i-5j6k-7l8m9n0o1p2q

import { useEffect, useState, useRef } from 'react';
import {
  Box,
  Button,
  Chip,
  Paper,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Typography,
} from '@mui/material';
import RefreshIcon from '@mui/icons-material/Refresh';
import * as api from '../services/api';

const POLL_INTERVAL_MS = 5_000;

const getHitRateColor = (
  hitRate: number
): 'default' | 'primary' | 'success' | 'warning' | 'error' => {
  if (hitRate >= 0.8) return 'success';
  if (hitRate >= 0.5) return 'warning';
  if (hitRate > 0) return 'error';
  return 'default';
};

const formatDuration = (stats: api.CacheStatsEntry): string => {
  const { get_duration_seconds: dur } = stats;
  if (!dur || dur.count === 0) return '—';
  const avgSeconds = dur.sum / dur.count;
  const avgMicros = avgSeconds * 1_000_000;
  return `${avgMicros.toFixed(1)} µs`;
};

const formatMisses = (misses: api.CacheMisses): string => {
  if (!misses) return '—';
  const parts = Object.entries(misses)
    .filter(([, v]) => v > 0)
    .map(([k, v]) => `${k}: ${v}`);
  return parts.length > 0 ? parts.join(' / ') : '—';
};

const formatInvalidations = (inv: api.CacheInvalidations): string => {
  if (!inv) return '—';
  const parts = Object.entries(inv)
    .filter(([, v]) => v > 0)
    .map(([k, v]) => `${k}: ${v}`);
  return parts.length > 0 ? parts.join(' / ') : '—';
};

const totalRequests = (cache: api.CacheStatsEntry): number => {
  const misses = cache.misses
    ? Object.values(cache.misses).reduce((a, b) => a + b, 0)
    : 0;
  return cache.hits + misses;
};

const formatEvictions = (evict: api.CacheEvictions): string => {
  if (!evict) return '—';
  const parts = Object.entries(evict)
    .filter(([, v]) => v > 0)
    .map(([k, v]) => `${k}: ${v}`);
  return parts.length > 0 ? parts.join(' / ') : '—';
};

export function CacheStatsPanel() {
  const [stats, setStats] = useState<api.CacheStatsResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const isUnmountedRef = useRef(false);

  useEffect(() => {
    return () => {
      isUnmountedRef.current = true;
      if (intervalRef.current) clearInterval(intervalRef.current);
    };
  }, []);

  const fetchStats = async () => {
    if (isUnmountedRef.current) return;
    setLoading(true);
    try {
      const data = await api.getCacheStats();
      if (!isUnmountedRef.current) {
        setStats(data);
        setError(null);
      }
    } catch (e: unknown) {
      if (!isUnmountedRef.current) {
        setError(String(e));
      }
    } finally {
      if (!isUnmountedRef.current) {
        setLoading(false);
      }
    }
  };

  useEffect(() => {
    void fetchStats();
    if (intervalRef.current) clearInterval(intervalRef.current);
    intervalRef.current = setInterval(fetchStats, POLL_INTERVAL_MS);
    return () => {
      if (intervalRef.current) clearInterval(intervalRef.current);
    };
  }, []);

  return (
    <Paper sx={{ p: 2, mt: 3 }}>
      <Stack direction="row" justifyContent="space-between" alignItems="baseline" sx={{ mb: 2 }}>
        <Stack>
          <Typography variant="h6">Cache Stats</Typography>
          {stats?.generated_at && (
            <Typography variant="caption" color="text.secondary">
              Generated: {new Date(stats.generated_at).toLocaleString()}
            </Typography>
          )}
        </Stack>
        <Button
          size="small"
          variant="outlined"
          startIcon={<RefreshIcon />}
          onClick={fetchStats}
          disabled={loading}
        >
          {loading ? 'Refreshing...' : 'Refresh'}
        </Button>
      </Stack>

      {error && (
        <Typography color="error" variant="body2" sx={{ mb: 1 }}>
          {error}
        </Typography>
      )}

      <Box sx={{ overflowX: 'auto' }}>
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell>Cache</TableCell>
              <TableCell align="right">Size</TableCell>
              <TableCell align="right">Total</TableCell>
              <TableCell align="right">Hits</TableCell>
              <TableCell>Misses</TableCell>
              <TableCell align="center">Hit Rate</TableCell>
              <TableCell align="right">Sets</TableCell>
              <TableCell>Invalidations</TableCell>
              <TableCell>Evictions</TableCell>
              <TableCell align="right">Avg Get</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {!stats || !stats.caches?.length ? (
              <TableRow>
                <TableCell colSpan={10}>
                  <Typography variant="body2" color="text.secondary">
                    No cache stats available.
                  </Typography>
                </TableCell>
              </TableRow>
            ) : (
              stats.caches.map((cache) => (
                <TableRow key={cache.name}>
                  <TableCell>{cache.name}</TableCell>
                  <TableCell align="right">{cache.size}</TableCell>
                  <TableCell align="right">{totalRequests(cache)}</TableCell>
                  <TableCell align="right">{cache.hits}</TableCell>
                  <TableCell>{formatMisses(cache.misses)}</TableCell>
                  <TableCell align="center">
                    {cache.hit_rate !== undefined && cache.hit_rate !== null ? (
                      <Chip
                        size="small"
                        label={`${(cache.hit_rate * 100).toFixed(1)}%`}
                        color={getHitRateColor(cache.hit_rate)}
                      />
                    ) : (
                      <Typography variant="body2" color="text.secondary">
                        —
                      </Typography>
                    )}
                  </TableCell>
                  <TableCell align="right">{cache.sets}</TableCell>
                  <TableCell>{formatInvalidations(cache.invalidations)}</TableCell>
                  <TableCell>{formatEvictions(cache.evictions)}</TableCell>
                  <TableCell align="right">{formatDuration(cache)}</TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </Box>
    </Paper>
  );
}
