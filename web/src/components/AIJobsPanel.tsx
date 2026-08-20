// file: web/src/components/AIJobsPanel.tsx
// version: 1.0.3
// guid: 4a7b8c9d-0e1f-2a3b-4c5d-6e7f8a9b0c1d

import { useEffect, useState, useRef } from 'react';
import {
  Box,
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
import * as api from '../services/api';

const POLL_INTERVAL_MS = 15_000;

const statusColor = (s: string): 'default' | 'primary' | 'success' | 'warning' | 'error' => {
  switch (s) {
    case 'pending':
    case 'submitted':
      return 'primary';
    case 'completed':
      return 'success';
    case 'completed_with_errors':
      return 'warning';
    case 'failed':
    case 'expired':
      return 'error';
    default:
      return 'default';
  }
};

const formatTime = (s?: string): string => {
  if (!s) return '—';
  const d = new Date(s);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return '—';
  return d.toLocaleString();
};

export function AIJobsPanel() {
  const [jobs, setJobs] = useState<api.AIJob[]>([]);
  const [error, setError] = useState<string | null>(null);
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const isUnmountedRef = useRef(false);

  useEffect(() => {
    return () => {
      isUnmountedRef.current = true;
      if (intervalRef.current) clearInterval(intervalRef.current);
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      if (isUnmountedRef.current) return;
      try {
        const data = await api.listAIJobs({ limit: 50 });
        if (!cancelled && !isUnmountedRef.current) {
          setJobs(data);
          setError(null);
        }
      } catch (e: unknown) {
        if (!cancelled && !isUnmountedRef.current) setError(String(e));
      }
    };
    void load();
    if (intervalRef.current) clearInterval(intervalRef.current);
    intervalRef.current = setInterval(load, POLL_INTERVAL_MS);
    return () => {
      cancelled = true;
      if (intervalRef.current) clearInterval(intervalRef.current);
    };
  }, []);

  const inFlight = jobs.filter((j) => j.status === 'pending' || j.status === 'submitted').length;

  return (
    <Paper sx={{ p: 2, mt: 3 }}>
      <Stack
        direction="row"
        spacing={2}
        sx={{
          alignItems: 'baseline',
          mb: 1,
        }}
      >
        <Typography variant="h6">AI Jobs</Typography>
        <Typography
          variant="body2"
          sx={{
            color: 'text.secondary',
          }}
        >
          {inFlight} in flight · {jobs.length} recent
        </Typography>
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
              <TableCell>Type</TableCell>
              <TableCell>Status</TableCell>
              <TableCell align="right">Items</TableCell>
              <TableCell align="right">OK</TableCell>
              <TableCell align="right">Err</TableCell>
              <TableCell>Submitted</TableCell>
              <TableCell>Completed</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {jobs.length === 0 ? (
              <TableRow>
                <TableCell colSpan={7}>
                  <Typography
                    variant="body2"
                    sx={{
                      color: 'text.secondary',
                    }}
                  >
                    No AI jobs recorded yet.
                  </Typography>
                </TableCell>
              </TableRow>
            ) : (
              jobs.map((j) => (
                <TableRow key={j.id}>
                  <TableCell>{j.type}</TableCell>
                  <TableCell>
                    <Chip size="small" color={statusColor(j.status)} label={j.status} />
                  </TableCell>
                  <TableCell align="right">{j.item_count}</TableCell>
                  <TableCell align="right">{j.success_count}</TableCell>
                  <TableCell align="right">{j.error_count}</TableCell>
                  <TableCell>{formatTime(j.submitted_at)}</TableCell>
                  <TableCell>{formatTime(j.completed_at)}</TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </Box>
    </Paper>
  );
}
