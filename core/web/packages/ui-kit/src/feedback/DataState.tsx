// file: core/web/packages/ui-kit/src/feedback/DataState.tsx
// version: 0.1.0
// guid: 1b8e5d47-3c09-4a62-8f71-e2d6c4a9b305
// last-edited: 2026-11-20

import type { ReactNode } from 'react';
import { Alert, Box, Button, CircularProgress, Typography } from '@mui/material';

/**
 * The four states of a data-loading view, each distinct to the user: loading,
 * error (the message shown, with a retry), empty and populated. Presentational:
 * the caller owns the request.
 */
export type DataStatus = 'loading' | 'error' | 'empty' | 'ready';

export interface DataStateProps {
  status: DataStatus;
  error?: string;
  emptyText?: string;
  loadingText?: string;
  onRetry?: () => void;
  children?: ReactNode;
}

export function DataState({ status, error, emptyText = 'Nothing here yet.', loadingText = 'Loading…', onRetry, children }: DataStateProps) {
  switch (status) {
    case 'loading':
      return (
        <Box role="status" aria-live="polite" sx={{ display: 'flex', alignItems: 'center', gap: 2, p: 3 }}>
          <CircularProgress size={24} />
          <Typography>{loadingText}</Typography>
        </Box>
      );
    case 'error':
      return (
        <Alert
          severity="error"
          action={onRetry ? <Button color="inherit" size="small" onClick={onRetry}>Retry</Button> : undefined}
          sx={{ m: 2 }}
        >
          {error || 'The request failed.'}
        </Alert>
      );
    case 'empty':
      return (
        <Typography color="text.secondary" sx={{ p: 3 }}>
          {emptyText}
        </Typography>
      );
    default:
      return <>{children}</>;
  }
}

export interface BlockingPageProps {
  title: string;
  children?: ReactNode;
  action?: ReactNode;
}

/** A full-page message that replaces the app: a version mismatch, a refused mount. */
export function BlockingPage({ title, children, action }: BlockingPageProps) {
  return (
    <Box role="alert" sx={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', p: 3 }}>
      <Box sx={{ maxWidth: 560 }}>
        <Typography variant="h4" component="h1" gutterBottom>
          {title}
        </Typography>
        {children}
        {action && <Box sx={{ mt: 2 }}>{action}</Box>}
      </Box>
    </Box>
  );
}
