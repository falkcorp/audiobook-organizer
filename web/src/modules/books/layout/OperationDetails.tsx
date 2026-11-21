// file: web/src/modules/books/layout/OperationDetails.tsx
// version: 0.1.0
// guid: 8a5c1f37-6e92-4d04-a3b7-f0e2d9c4b618
// last-edited: 2026-11-21

import { Box, Typography } from '@mui/material';
import type { OperationsFrameRow } from '../../../components/layout/OperationsIndicatorFrame';

function parseMessageDetails(message: string) {
  const titleMatch = message.match(/—\s*(.+)$/);
  const countsMatch = message.match(/\(imported (\d+), skipped (\d+), failed (\d+)\)/);
  // OL import: "Importing editions: 1234k records"
  const olMatch = message.match(/Importing (\w+): (\d+)k records/);
  return {
    currentTitle: titleMatch ? titleMatch[1] : null,
    imported: countsMatch ? parseInt(countsMatch[1]) : null,
    skipped: countsMatch ? parseInt(countsMatch[2]) : null,
    failed: countsMatch ? parseInt(countsMatch[3]) : null,
    olType: olMatch ? olMatch[1] : null,
    olRecords: olMatch ? `${olMatch[2]}k` : null,
  };
}

/**
 * The lines a running import shows under its progress counts, parsed from the
 * server's progress message: imported / skipped / failed, an Open Library
 * import line, and the title being processed. What the bell rendered inline
 * before it became a frame with a `renderDetails` slot.
 */
export function BooksOperationDetails({ op }: { op: OperationsFrameRow }) {
  const details = parseMessageDetails(op.message);
  return (
    <>
      {details.imported !== null && (
        <Typography variant="caption" sx={{ display: 'block', mb: 0.25 }}>
          <Box component="span" sx={{ color: 'success.main' }}>
            {details.imported} imported
          </Box>
          {details.skipped! > 0 && (
            <Box component="span" sx={{ color: 'text.secondary', ml: 1 }}>
              {details.skipped} skipped
            </Box>
          )}
          {details.failed! > 0 && (
            <Box component="span" sx={{ color: 'error.main', ml: 1 }}>
              {details.failed} failed
            </Box>
          )}
        </Typography>
      )}

      {details.olType && (
        <Typography
          variant="caption"
          sx={{
            color: 'info.main',
            display: 'block',
            mb: 0.25,
          }}
        >
          {details.olType}: {details.olRecords} records
        </Typography>
      )}

      {details.currentTitle && (
        <Typography
          variant="caption"
          noWrap
          title={details.currentTitle}
          sx={{
            color: 'primary.main',
            display: 'block',
          }}
        >
          {details.currentTitle}
        </Typography>
      )}
    </>
  );
}
