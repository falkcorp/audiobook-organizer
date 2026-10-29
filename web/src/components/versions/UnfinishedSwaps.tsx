// file: web/src/components/versions/UnfinishedSwaps.tsx
// version: 1.0.1
// guid: f5d37b28-9f17-4cef-bd0d-f0e776f027e8
// last-edited: 2026-10-29

import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';
import {
  Alert,
  Box,
  Button,
  Chip,
  Link,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from '@mui/material';
import { isAuthRedirectError } from '../../utils/apiFetch';
import { describeRequestError, SESSION_EXPIRED_MESSAGE } from '../../utils/apiResponse';
import {
  listSwapIncompleteBooks,
  resumeVersionSwap,
  type ResumeSwapResult,
  type SwapIncompleteBook,
} from '../../services/versionApi';

/** How many file paths of a version are listed before the rest is summarised. */
const FILE_CAP = 3;

// What a resume of the book would do, said before the person confirms it.
function resumeLabel(b: SwapIncompleteBook): string {
  return b.resume === 'undo' ? 'Resume (move files back)' : 'Resume (finish swap)';
}

function resumeConfirmText(b: SwapIncompleteBook): string {
  const what =
    b.resume === 'undo'
      ? 'This puts the files back where they were and makes the version that was leaving the active one again.'
      : 'This finishes the swap: the version that was entering becomes the active one and the other becomes an alternate. No file is moved.';
  // The list was read when the page loaded; the server reads the disk and the
  // rows again when the button is clicked, and does what it finds then.
  const replan =
    ' The server works out what to do again when you confirm, from what is on disk at that moment, so it may do the other of the two (or refuse and change nothing) if the book has changed since this list was loaded.';
  return `Resume the unfinished swap of "${b.title || b.book_id}"? ${what}${replan}`;
}

function describeResult(r: ResumeSwapResult): string {
  const moved =
    r.files_moved > 0 ? `, ${r.files_moved} file${r.files_moved === 1 ? '' : 's'} moved back` : '';
  return r.action === 'undo'
    ? `The swap was undone${moved}.`
    : 'The swap was finished; the book plays the version that was entering.';
}

interface UnfinishedSwapsProps {
  /**
   * The version ids a trash cleanup run named: only the books that have one of
   * them are listed. null lists every book with an unfinished swap.
   */
  onlyVersionIds: string[] | null;
}

/**
 * The books whose version swap did not finish (a version left in swapping_out /
 * swapping_in), each with its files and a Resume button. Such a book has no
 * active version, and neither it nor its versions can be trashed, until the swap
 * is resumed: the server finishes it when no file needs to move and otherwise
 * puts the files back, and refuses (saying why) when the disk and the records
 * disagree in a way it will not guess at.
 */
export default function UnfinishedSwaps({ onlyVersionIds }: UnfinishedSwapsProps) {
  const [books, setBooks] = useState<SwapIncompleteBook[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');
  const [actionError, setActionError] = useState('');
  const [done, setDone] = useState('');
  const [busy, setBusy] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError('');
    try {
      setBooks(await listSwapIncompleteBooks());
    } catch (err) {
      setBooks([]);
      setLoadError(describeRequestError(err, 'Failed to load the unfinished swaps'));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const listed = useMemo(() => {
    if (onlyVersionIds === null) return books;
    const wanted = new Set(onlyVersionIds);
    return books.filter((b) => b.versions.some((v) => wanted.has(v.id)));
  }, [books, onlyVersionIds]);
  // The run named books that are no longer stuck (resumed since).
  const gone =
    onlyVersionIds === null || loading || loadError !== ''
      ? 0
      : Math.max(
          0,
          new Set(
            onlyVersionIds.filter((id) => !books.some((b) => b.versions.some((v) => v.id === id)))
          ).size
        );

  const handleResume = useCallback(
    async (b: SwapIncompleteBook) => {
      if (!confirm(resumeConfirmText(b))) return;
      setActionError('');
      setDone('');
      setBusy(b.book_id);
      try {
        const r = await resumeVersionSwap(b.book_id);
        setDone(`${b.title || b.book_id}: ${describeResult(r)}`);
      } catch (err) {
        setActionError(
          isAuthRedirectError(err)
            ? SESSION_EXPIRED_MESSAGE
            : `Resume failed: ${err instanceof Error && err.message ? err.message : 'request failed'}`
        );
      } finally {
        setBusy('');
      }
      load();
    },
    [load]
  );

  return (
    <Box data-testid="unfinished-swaps">
      <Alert severity="warning" sx={{ mb: 2 }}>
        A swap that stopped half way leaves its book with no active version. Until it is resumed
        nothing of that book can be trashed. The nightly cleanup lists these books but never resumes
        one by itself, because resuming can move files.
      </Alert>

      {done && (
        <Alert severity="success" sx={{ mb: 2 }} onClose={() => setDone('')}>
          {done}
        </Alert>
      )}
      {actionError && (
        <Alert severity="error" sx={{ mb: 2 }} onClose={() => setActionError('')}>
          {actionError}
        </Alert>
      )}
      {gone > 0 && (
        <Typography variant="body2" sx={{ color: 'text.secondary', mb: 1 }}>
          {`${gone.toLocaleString()} ${gone === 1 ? 'version' : 'versions'} of that cleanup run ${gone === 1 ? 'is' : 'are'} not in an unfinished swap any more.`}
        </Typography>
      )}

      {loading ? (
        <Typography sx={{ color: 'text.secondary' }}>Loading...</Typography>
      ) : loadError ? (
        <Alert severity="error">{loadError}</Alert>
      ) : listed.length === 0 ? (
        <Typography sx={{ color: 'text.secondary' }}>No unfinished swaps.</Typography>
      ) : (
        <TableContainer component={Paper}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Book</TableCell>
                <TableCell>Versions and files</TableCell>
                <TableCell align="right" sx={{ width: 260 }}>
                  Actions
                </TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {listed.map((b) => (
                <TableRow key={b.book_id} data-testid={`unfinished-swap-${b.book_id}`}>
                  <TableCell sx={{ verticalAlign: 'top' }}>
                    <Link component={RouterLink} to={`/library/${encodeURIComponent(b.book_id)}`}>
                      {b.title || b.book_id}
                    </Link>
                    <Typography
                      variant="caption"
                      component="div"
                      sx={{
                        fontFamily: 'monospace',
                        wordBreak: 'break-all',
                        color: 'text.secondary',
                      }}
                    >
                      {b.book_path || 'no path'}
                    </Typography>
                  </TableCell>
                  <TableCell sx={{ verticalAlign: 'top' }}>
                    {b.versions.map((v) => (
                      <Box key={v.id} sx={{ mb: 1 }}>
                        <Chip
                          size="small"
                          label={v.status}
                          color={v.status.startsWith('swapping') ? 'warning' : 'default'}
                          sx={{ mr: 1 }}
                        />
                        <Typography variant="caption" sx={{ fontFamily: 'monospace' }}>
                          {v.id.slice(0, 12)}
                        </Typography>
                        {v.files.slice(0, FILE_CAP).map((f) => (
                          <Typography
                            key={f.id}
                            variant="caption"
                            component="div"
                            sx={{ pl: 1, fontFamily: 'monospace', wordBreak: 'break-all' }}
                          >
                            {f.path}
                          </Typography>
                        ))}
                        {v.files.length > FILE_CAP && (
                          <Typography variant="caption" component="div" sx={{ pl: 1 }}>
                            {`and ${(v.files.length - FILE_CAP).toLocaleString()} more`}
                          </Typography>
                        )}
                      </Box>
                    ))}
                  </TableCell>
                  <TableCell align="right" sx={{ verticalAlign: 'top' }}>
                    {b.resume ? (
                      <Button
                        size="small"
                        variant="outlined"
                        disabled={busy !== ''}
                        onClick={() => handleResume(b)}
                      >
                        {resumeLabel(b)}
                      </Button>
                    ) : (
                      <Typography
                        variant="caption"
                        sx={{ color: 'error.main' }}
                        data-testid="resume-refusal"
                      >
                        {b.resume_refusal || 'A resume would be refused.'}
                      </Typography>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Box>
  );
}
