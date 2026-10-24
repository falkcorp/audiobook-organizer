// file: web/src/components/bookdetail/RetiredVersionsPanel.tsx
// version: 1.1.0
// guid: 6bf6e70d-098a-4a16-b274-4894ef465062
// last-edited: 2026-10-23

import { Fragment, useEffect, useState } from 'react';
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Alert,
  Box,
  Button,
  Chip,
  Link as MuiLink,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Tooltip,
  Typography,
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import { Link as RouterLink } from 'react-router-dom';
import { listRetiredVersions, type RetiredVersion } from '../../services/versionApi';
import { describeRequestError } from '../../utils/apiResponse';
import { useCanOrganizeLibrary } from '../../hooks/useCanOrganizeLibrary';
import { formatBytes, formatDateTime, formatDuration } from './bookDetailUtils';

interface RetiredVersionsPanelProps {
  /** The book being viewed: the holder the retired versions were moved onto. */
  bookId: string;
  /** Bumped by the page to make the panel reload (for example after a revert). */
  refreshKey?: number;
}

/**
 * Why Restore is not available for v, shown as the disabled button's tooltip.
 * Restoring is not available at all yet: the server answers its restore route
 * with 501 until restores are journaled (a direct restore would drop each
 * file's version link and leave no undo), so the last reason always applies.
 * The more specific reasons come first because they stay true after journaling
 * lands; the journaled restore adds a null result (no reason, button enabled)
 * for a version that can be restored.
 */
function restoreBlockedReason(v: RetiredVersion): string {
  if (!v.shell_exists) {
    return 'The book these files were retired from has been purged, so they cannot be restored here yet.';
  }
  if (!v.shell_in_trash) {
    return 'The book these files were retired from is no longer in the trash, so they cannot be restored to it.';
  }
  return 'Restore will be available once restores are journaled.';
}

// RetiredVersionsPanel lists the retired versions a book holds: the files of a
// trashed or merged book that the retire-versions repair moved onto this one so
// the trash could be emptied without deleting any file row. Restoring a version
// (moving its files back to the book they came from) is shown but disabled with
// its reason until restores are journaled; the button is not shown at all to a
// caller without the library-organize permission the restore will need.
//
// Collapsed by default: a retired version is not part of the book's playable
// files, so it must not compete with them for attention.
export function RetiredVersionsPanel({ bookId, refreshKey = 0 }: RetiredVersionsPanelProps) {
  const canOrganize = useCanOrganizeLibrary();
  const [versions, setVersions] = useState<RetiredVersion[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [loadError, setLoadError] = useState('');
  const [expanded, setExpanded] = useState(false);
  const [openFiles, setOpenFiles] = useState<Set<string>>(new Set());

  // Fetch on mount and when the page bumps refreshKey. The page keys this
  // component by book, so state never carries over from another book; a
  // response that arrives after unmount or a newer fetch is dropped.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const result = await listRetiredVersions(bookId);
        if (cancelled) return;
        setVersions(result);
        setLoadError('');
      } catch (err) {
        // An expired session lands here too; show it instead of an empty list
        // that reads as "no retired versions".
        if (!cancelled) setLoadError(describeRequestError(err, 'Failed to load retired versions'));
      } finally {
        if (!cancelled) setLoaded(true);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [bookId, refreshKey]);

  const toggleFiles = (versionId: string) => {
    setOpenFiles((prev) => {
      const next = new Set(prev);
      if (next.has(versionId)) next.delete(versionId);
      else next.add(versionId);
      return next;
    });
  };

  const count = loaded && !loadError ? ` (${versions.length})` : '';

  return (
    <Accordion
      expanded={expanded}
      onChange={(_, open) => setExpanded(open)}
      disableGutters
      sx={{ mt: 2 }}
      data-testid="retired-versions-panel"
    >
      <AccordionSummary expandIcon={<ExpandMoreIcon />}>
        <Typography variant="subtitle1" sx={{ fontWeight: 'bold' }}>
          Retired versions{count}
        </Typography>
      </AccordionSummary>
      <AccordionDetails>
        <Typography variant="body2" sx={{ color: 'text.secondary', mb: 1 }}>
          Files of trashed or merged books that were moved onto this book so the trash could be
          emptied. They are not part of this book&apos;s files and are never played.
        </Typography>
        {!loaded ? (
          <Typography sx={{ color: 'text.secondary' }}>Loading...</Typography>
        ) : loadError ? (
          <Alert severity="error">{loadError}</Alert>
        ) : versions.length === 0 ? (
          <Typography sx={{ color: 'text.secondary' }}>No retired versions.</Typography>
        ) : (
          <TableContainer>
            <Table size="small">
              <TableHead>
                <TableRow>
                  <TableCell>Retired from</TableCell>
                  <TableCell>Retired</TableCell>
                  <TableCell align="right">Files</TableCell>
                  <TableCell align="right">Size</TableCell>
                  <TableCell align="right">Duration</TableCell>
                  <TableCell align="right">Actions</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {versions.map((v) => {
                  const filesOpen = openFiles.has(v.id);
                  return (
                    <Fragment key={v.id}>
                      <TableRow data-testid={`retired-version-${v.id}`}>
                        <TableCell sx={{ wordBreak: 'break-all' }}>
                          {v.shell_exists && v.retired_from_book_id ? (
                            <MuiLink component={RouterLink} to={`/library/${v.retired_from_book_id}`}>
                              {v.retired_from_title || v.retired_from_book_id}
                            </MuiLink>
                          ) : (
                            <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
                              <Typography variant="body2" sx={{ fontFamily: 'monospace' }}>
                                {v.retired_from_book_id || 'unknown'}
                              </Typography>
                              <Chip label="purged" size="small" variant="outlined" color="warning" />
                            </Stack>
                          )}
                          {v.source_original_path && (
                            <Typography
                              variant="caption"
                              sx={{ display: 'block', color: 'text.secondary' }}
                            >
                              {v.source_original_path}
                            </Typography>
                          )}
                        </TableCell>
                        <TableCell>{formatDateTime(v.retired_at)}</TableCell>
                        <TableCell align="right">{v.file_count}</TableCell>
                        <TableCell align="right">{formatBytes(v.total_size)}</TableCell>
                        <TableCell align="right">{formatDuration(v.total_duration)}</TableCell>
                        <TableCell align="right">
                          <Stack direction="row" spacing={1} sx={{ justifyContent: 'flex-end' }}>
                            <Button size="small" onClick={() => toggleFiles(v.id)}>
                              {filesOpen ? 'Hide files' : 'Show files'}
                            </Button>
                            {canOrganize && (
                              <Tooltip title={restoreBlockedReason(v)}>
                                <span>
                                  <Button size="small" variant="outlined" disabled>
                                    Restore
                                  </Button>
                                </span>
                              </Tooltip>
                            )}
                          </Stack>
                        </TableCell>
                      </TableRow>
                      {filesOpen && (
                        <TableRow>
                          <TableCell colSpan={6} sx={{ py: 0.5 }}>
                            <Box component="ul" sx={{ m: 0, pl: 2 }} data-testid={`retired-files-${v.id}`}>
                              {v.files.map((f) => (
                                <Typography
                                  component="li"
                                  key={f.id}
                                  variant="body2"
                                  sx={{
                                    fontFamily: 'monospace',
                                    fontSize: '0.8rem',
                                    wordBreak: 'break-all',
                                    color: f.missing ? 'error.main' : undefined,
                                  }}
                                >
                                  {f.file_path}
                                  {f.missing ? ' (missing)' : ''}
                                </Typography>
                              ))}
                            </Box>
                          </TableCell>
                        </TableRow>
                      )}
                    </Fragment>
                  );
                })}
              </TableBody>
            </Table>
          </TableContainer>
        )}
      </AccordionDetails>
    </Accordion>
  );
}
