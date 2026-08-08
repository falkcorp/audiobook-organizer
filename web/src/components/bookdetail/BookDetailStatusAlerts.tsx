// file: web/src/components/bookdetail/BookDetailStatusAlerts.tsx
// version: 1.0.1
// guid: c3d4e5f6-a7b8-9012-cdef-123456789012
// last-edited: 2026-08-07

import { Alert, Box, Button, Stack, Tooltip } from '@mui/material';
import WarningAmberIcon from '@mui/icons-material/WarningAmber';
import type { Book } from '../../services/api';
import { formatDateTime } from './bookDetailUtils';

export interface BookDetailStatusAlertsProps {
  book: Book;
  actionLoading: boolean;
  onRestore: () => void;
  onUnquarantine: () => void;
  onOpenPurgeDialog: () => void;
  onQuarantine: () => void;
}

export const BookDetailStatusAlerts = ({
  book,
  actionLoading,
  onRestore,
  onUnquarantine,
  onOpenPurgeDialog,
  onQuarantine,
}: BookDetailStatusAlertsProps) => {
  const isSoftDeleted = book.marked_for_deletion;
  const isQuarantined = !!book.quarantined_at;

  return (
    <>
      {isSoftDeleted && (
        <Alert
          severity="warning"
          action={
            <Stack direction="row" spacing={1}>
              <Button
                color="inherit"
                size="small"
                onClick={onRestore}
                disabled={actionLoading}
              >
                Restore
              </Button>
              <Button
                color="inherit"
                size="small"
                onClick={onOpenPurgeDialog}
                disabled={actionLoading}
              >
                Purge
              </Button>
            </Stack>
          }
          sx={{ mb: 2 }}
        >
          Marked for deletion on {formatDateTime(book.marked_for_deletion_at)}.
          Last updated {formatDateTime(book.updated_at)}.
          Restore to keep the book or purge to remove it permanently.
        </Alert>
      )}

      {isQuarantined && (
        <Alert
          severity="error"
          action={
            <Button
              color="inherit"
              size="small"
              onClick={onUnquarantine}
              disabled={actionLoading}
            >
              Restore
            </Button>
          }
          sx={{ mb: 2 }}
        >
          Quarantined on {formatDateTime(book.quarantined_at)}.
          Reason: {book.quarantine_reason || 'unknown'}.
          File is in .failed/ and excluded from scans and write-back.
        </Alert>
      )}

      {!isQuarantined && (
        <Box sx={{ mb: 2, display: 'flex', justifyContent: 'flex-end' }}>
          <Tooltip title="Move to .failed/ — excludes from scans and iTunes">
            <Button
              size="small"
              color="error"
              variant="outlined"
              startIcon={<WarningAmberIcon />}
              onClick={onQuarantine}
              disabled={actionLoading}
            >
              Quarantine
            </Button>
          </Tooltip>
        </Box>
      )}

      {book.file_exists === false && (
        <Alert severity="error" sx={{ mb: 2 }}>
          File missing: The audio file at <strong>{book.file_path}</strong> could not be found on disk.
          The file may have been moved, renamed, or deleted.
        </Alert>
      )}

      {book.metadata_source_hash && (book.metadata_source_hash_duplicate_count ?? 0) > 0 && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          ⚠ This book shares its metadata source with{' '}
          <strong>{book.metadata_source_hash_duplicate_count}</strong>{' '}
          other book{(book.metadata_source_hash_duplicate_count ?? 0) === 1 ? '' : 's'} — possible duplicate.{' '}
          <a href="/dedup/candidates" style={{ color: 'inherit', fontWeight: 'bold' }}>
            View duplicates
          </a>
        </Alert>
      )}
    </>
  );
};
