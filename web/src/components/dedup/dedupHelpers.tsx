// file: web/src/components/dedup/dedupHelpers.tsx
// version: 1.1.1
// guid: 8C089ABB-8110-41B2-A660-7064FB18C63A
// last-edited: 2026-08-19

import { useState, useEffect } from 'react';
import { Paper, Stack, Box, Typography, LinearProgress, TablePagination } from '@mui/material';
import * as api from '../../services/api';
import type { Operation } from '../../services/api';

export function cleanDisplayTitle(title: string): string {
  return title
    .replace(/\s*\((un)?abridged\)/gi, '')
    .replace(/^\[.*?\]\s*/g, '')
    .trim();
}

export function OperationProgress({
  operation,
  label,
}: {
  operation: Operation | null;
  label?: string;
}) {
  // Hide the progress bar once the operation has stopped. This used to spell
  // the cancelled state 'cancelled' (two Ls) while the backend mints
  // 'canceled', so a cancelled operation kept showing a progress bar that would
  // never advance again.
  if (!operation || api.isOperationTerminal(operation.status)) return null;
  const pct = operation.total > 0 ? Math.round((operation.progress / operation.total) * 100) : 0;
  return (
    <Paper sx={{ p: 2, mb: 2 }}>
      <Stack spacing={1}>
        {label && (
          <Typography variant="caption" color="text.secondary" fontWeight="bold">
            {label}
          </Typography>
        )}
        <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <Typography variant="body2">{operation.message || 'Processing...'}</Typography>
          <Typography variant="caption">{pct}%</Typography>
        </Box>
        <LinearProgress
          variant={operation.total > 0 ? 'determinate' : 'indeterminate'}
          value={pct}
        />
      </Stack>
    </Paper>
  );
}

export async function runOperationWithPolling(
  startFn: () => Promise<Operation>,
  setOp: (op: Operation | null) => void,
  onComplete: (op: Operation) => void,
  onError: (msg: string) => void
) {
  try {
    const initial = await startFn();
    setOp(initial);
    const final = await api.pollOperation(initial.id, (update) => setOp(update));
    setOp(null);
    onComplete(final);
  } catch (err) {
    setOp(null);
    onError(err instanceof Error ? err.message : 'Operation failed');
  }
}

export const PAGE_SIZE_OPTIONS = [10, 25, 50, 100];

export function usePagination(totalItems: number) {
  const [page, setPage] = useState(0);
  const [rowsPerPage, setRowsPerPage] = useState(25);

  useEffect(() => {
    setPage(0);
  }, [totalItems]);

  const startIdx = page * rowsPerPage;
  const endIdx = startIdx + rowsPerPage;

  return { page, setPage, rowsPerPage, setRowsPerPage, startIdx, endIdx };
}

export function PaginationControls({
  total,
  page,
  rowsPerPage,
  onPageChange,
  onRowsPerPageChange,
}: {
  total: number;
  page: number;
  rowsPerPage: number;
  onPageChange: (page: number) => void;
  onRowsPerPageChange: (rpp: number) => void;
}) {
  if (total <= PAGE_SIZE_OPTIONS[0]) return null;
  return (
    <TablePagination
      component="div"
      count={total}
      page={page}
      onPageChange={(_, p) => onPageChange(p)}
      rowsPerPage={rowsPerPage}
      onRowsPerPageChange={(e) => {
        onRowsPerPageChange(parseInt(e.target.value, 10));
        onPageChange(0);
      }}
      rowsPerPageOptions={PAGE_SIZE_OPTIONS}
      labelRowsPerPage="Groups per page:"
      sx={{ mt: 2 }}
    />
  );
}
