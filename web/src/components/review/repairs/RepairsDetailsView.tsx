// file: web/src/components/review/repairs/RepairsDetailsView.tsx
// version: 1.0.0
// guid: 4b556cac-7a63-4f88-901b-ecfe5f1bdb62
// last-edited: 2026-10-08

/**
 * Details: one card per repairs row with everything known about it -- the
 * fields as they are now beside what the repair leaves, every book of the row
 * with its library metadata, each book's files on demand, and the full reason
 * and evidence.
 *
 * Book metadata is ONE request per page (getBooksByIds over every member id
 * on the page), aborted when the page changes. Files are fetched per book only
 * when asked for, from the book's file rows (never book.file_path, which is
 * stale). Every fetch shows loading, failure (with Retry) and empty
 * differently, and is cut off after FETCH_TIMEOUT_MS so a hung server reads
 * as an error rather than a spinner forever.
 */

import { useEffect, useMemo, useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';
import { Alert, Box, Button, Chip, CircularProgress, Link, Stack, Typography } from '@mui/material';
import * as api from '../../../services/api';
import type { Book, BookFile, RepairRow, RepairRowMember } from '../../../services/api';
import {
  OutcomeChip,
  OwnerApplyCell,
  RiskChip,
  RowCheckbox,
  RowEvidence,
  RowTitle,
  type RepairsViewProps,
} from './RowParts';
import {
  afterValue,
  classLabel,
  formatHMM,
  isSkippedFilter,
  memberCounts,
  nowValue,
  rowFieldKeys,
  rowMembers,
  rowReason,
} from './rowHelpers';

/** How long a book or file fetch may take before it is reported as failed. */
export const FETCH_TIMEOUT_MS = 30_000;

function errorText(err: unknown, timedOut: boolean, what: string): string {
  if (timedOut) return `Loading ${what} timed out after ${FETCH_TIMEOUT_MS / 1000} s.`;
  return err instanceof Error && err.message ? err.message : `Could not load ${what}.`;
}

interface BooksState {
  /** The request this state answers: member ids plus the retry count. */
  key: string;
  books?: ReadonlyMap<string, Book>;
  error?: string;
}

/** Library metadata for every member of the page's rows, one request per page. */
function usePageBooks(rows: RepairRow[]) {
  const ids = useMemo(
    () => [...new Set(rows.flatMap((r) => rowMembers(r).map((m) => m.book_id)))].sort().join(','),
    [rows]
  );
  const [nonce, setNonce] = useState(0);
  const [state, setState] = useState<BooksState>({ key: '' });
  const key = `${ids}#${nonce}`;

  useEffect(() => {
    if (!ids) return;
    const ctrl = new AbortController();
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      ctrl.abort();
    }, FETCH_TIMEOUT_MS);
    api
      .getBooksByIds(ids.split(','), ctrl.signal)
      .then((books) => {
        if (ctrl.signal.aborted) return;
        setState({ key, books: new Map(books.map((b) => [b.id, b])) });
      })
      .catch((err: unknown) => {
        // The page changed (or the view closed): not a failure to show.
        if (ctrl.signal.aborted && !timedOut) return;
        setState({ key, error: errorText(err, timedOut, 'book details') });
      })
      .finally(() => clearTimeout(timer));
    return () => {
      clearTimeout(timer);
      ctrl.abort();
    };
  }, [ids, key]);

  const current = state.key === key ? state : null;
  return {
    hasIds: ids !== '',
    loading: ids !== '' && current === null,
    books: current?.books ?? null,
    error: current?.error ?? null,
    retry: () => setNonce((n) => n + 1),
  };
}

type FilesState =
  | { phase: 'idle' }
  | { phase: 'loading'; nonce: number }
  | { phase: 'failed'; message: string }
  | { phase: 'loaded'; files: BookFile[]; count: number };

/** A member's files, fetched only when "Show files" is pressed. */
function MemberFiles({ rowId, bookId }: { rowId: string; bookId: string }) {
  const [state, setState] = useState<FilesState>({ phase: 'idle' });
  const loadingNonce = state.phase === 'loading' ? state.nonce : null;

  useEffect(() => {
    if (loadingNonce === null) return;
    const ctrl = new AbortController();
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      ctrl.abort();
    }, FETCH_TIMEOUT_MS);
    api
      .getBookFiles(bookId, { signal: ctrl.signal })
      .then((res) => {
        if (ctrl.signal.aborted) return;
        setState({ phase: 'loaded', files: res?.files ?? [], count: res?.count ?? 0 });
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted && !timedOut) return;
        setState({ phase: 'failed', message: errorText(err, timedOut, 'files') });
      })
      .finally(() => clearTimeout(timer));
    return () => {
      clearTimeout(timer);
      ctrl.abort();
    };
  }, [bookId, loadingNonce]);

  const load = () => setState({ phase: 'loading', nonce: Date.now() });
  const tid = `repairs-member-files-${rowId}-${bookId}`;

  if (state.phase === 'idle') {
    return (
      <Button size="small" onClick={load} sx={{ p: 0, minWidth: 0, textTransform: 'none' }} data-testid={`${tid}-show`}>
        Show files
      </Button>
    );
  }
  if (state.phase === 'loading') {
    return (
      <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }} data-testid={`${tid}-loading`}>
        <CircularProgress size={14} />
        <Typography variant="caption">Loading files…</Typography>
      </Stack>
    );
  }
  if (state.phase === 'failed') {
    return (
      <Alert
        severity="error"
        sx={{ py: 0 }}
        data-testid={`${tid}-error`}
        action={
          <Button color="inherit" size="small" onClick={load}>
            Retry
          </Button>
        }
      >
        {state.message}
      </Alert>
    );
  }
  return (
    <Box data-testid={tid}>
      <Button
        size="small"
        onClick={() => setState({ phase: 'idle' })}
        sx={{ p: 0, minWidth: 0, textTransform: 'none' }}
      >
        Hide files
      </Button>
      {state.files.length === 0 ? (
        <Typography variant="caption" component="div" sx={{ color: 'text.secondary' }}>
          No files on record for this book.
        </Typography>
      ) : (
        <Box component="ul" sx={{ m: 0, pl: 2 }}>
          {state.files.map((f) => (
            <Typography
              key={f.id}
              component="li"
              variant="caption"
              sx={{ wordBreak: 'break-all', color: f.file_exists === false ? 'error.main' : 'text.primary' }}
            >
              {f.file_path}
              {f.file_exists === false && ' (missing on disk)'}
            </Typography>
          ))}
        </Box>
      )}
      {state.count > state.files.length && (
        <Typography variant="caption" component="div" sx={{ color: 'text.secondary' }}>
          Showing {state.files.length} of {state.count} files.
        </Typography>
      )}
    </Box>
  );
}

function bookMeta(book: Book): string {
  const series = book.series_name
    ? `${book.series_name}${
        book.series_position_raw || book.series_sequence != null
          ? ` #${book.series_position_raw || book.series_sequence}`
          : ''
      }`
    : '';
  return [
    book.author_name,
    book.narrator ? `read by ${book.narrator}` : '',
    series,
    formatHMM(book.duration),
  ]
    .filter(Boolean)
    .join(' · ');
}

function MemberLine({
  row,
  member,
  books,
}: {
  row: RepairRow;
  member: RepairRowMember;
  books: ReturnType<typeof usePageBooks>;
}) {
  const book = books.books?.get(member.book_id);
  const counts = memberCounts(member);
  return (
    <Box component="li" sx={{ mb: 0.5 }} data-testid={`repairs-member-${row.row_id}-${member.book_id}`}>
      <Link component={RouterLink} to={`/library/${encodeURIComponent(member.book_id)}`}>
        {member.title || book?.title || member.book_id}
      </Link>
      {counts && (
        <Typography variant="caption" sx={{ color: 'text.secondary', ml: 0.5 }}>
          {counts}
        </Typography>
      )}
      <Typography variant="body2" component="div" sx={{ color: 'text.secondary' }}>
        {books.loading
          ? 'Loading book details…'
          : books.error
            ? 'Book details could not be loaded.'
            : book
              ? bookMeta(book) || 'No author, narrator, series or duration on record.'
              : 'Book not found in the library.'}
      </Typography>
      <MemberFiles rowId={row.row_id} bookId={member.book_id} />
    </Box>
  );
}

function FieldColumn({
  heading,
  row,
  keys,
  value,
  testid,
}: {
  heading: string;
  row: RepairRow;
  keys: string[];
  value: (row: RepairRow, key: string) => string;
  testid: string;
}) {
  return (
    <Box sx={{ flex: 1, minWidth: 0 }} data-testid={testid}>
      <Typography variant="overline">{heading}</Typography>
      {keys.length === 0 ? (
        <Typography variant="body2">—</Typography>
      ) : (
        keys.map((k) => {
          const changed = nowValue(row, k) !== afterValue(row, k);
          return (
            <Typography key={k} variant="body2" sx={{ wordBreak: 'break-word' }}>
              <Box component="span" sx={{ color: 'text.secondary' }}>
                {k}:
              </Box>{' '}
              <Box component="span" sx={{ fontWeight: changed ? 600 : 400 }}>
                {value(row, k)}
              </Box>
            </Typography>
          );
        })
      )}
    </Box>
  );
}

function DetailsCard({
  row,
  repairs,
  books,
}: { row: RepairRow; books: ReturnType<typeof usePageBooks> } & RepairsViewProps) {
  const skippedTab = isSkippedFilter(repairs.filter);
  const outcome = repairs.rowOutcomes.get(row.row_id);
  const members = rowMembers(row);
  return (
    <Box
      data-testid={`repairs-row-${row.row_id}`}
      sx={{ m: 1, p: 1.5, border: 1, borderColor: 'divider', borderRadius: 1 }}
    >
      <Stack direction="row" spacing={1} useFlexGap sx={{ alignItems: 'center', flexWrap: 'wrap' }}>
        {!skippedTab && <RowCheckbox row={row} repairs={repairs} />}
        <Typography variant="subtitle1" component="div" sx={{ flex: 1, minWidth: 0 }}>
          <RowTitle row={row} />
          {row.author && (
            <Typography component="span" variant="body2" sx={{ color: 'text.secondary', ml: 1 }}>
              {row.author}
            </Typography>
          )}
        </Typography>
        {row.class && <Chip size="small" variant="outlined" label={classLabel(row.class)} />}
        <RiskChip risk={row.risk} />
        {!skippedTab && outcome && <OutcomeChip result={outcome} />}
      </Stack>

      <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2} sx={{ mt: 1 }}>
        <FieldColumn
          heading="Now"
          row={row}
          keys={Object.keys(row.current ?? {}).sort()}
          value={nowValue}
          testid={`repairs-row-now-${row.row_id}`}
        />
        <FieldColumn
          heading="After"
          row={row}
          keys={rowFieldKeys(row)}
          value={afterValue}
          testid={`repairs-row-after-${row.row_id}`}
        />
      </Stack>

      <Box sx={{ mt: 1 }}>
        <Typography variant="overline">
          {members.length === 1 ? 'Book' : `Books (${members.length})`}
        </Typography>
        <Box component="ul" sx={{ m: 0, pl: 2 }}>
          {members.map((m) => (
            <MemberLine key={m.book_id} row={row} member={m} books={books} />
          ))}
        </Box>
      </Box>

      <Box sx={{ mt: 1 }}>
        <Typography variant="overline">{skippedTab ? 'Why skipped' : 'Reason'}</Typography>
        <Typography variant="body2" sx={{ wordBreak: 'break-word' }}>
          {rowReason(row, skippedTab)}
        </Typography>
        {skippedTab && row.skip_reason && row.skipped && (
          <Typography variant="caption" component="div" sx={{ color: 'text.secondary' }}>
            {row.skipped}
          </Typography>
        )}
      </Box>
      {(row.evidence?.length ?? 0) > 0 && (
        <Box sx={{ mt: 1 }}>
          <Typography variant="overline">Evidence</Typography>
          <RowEvidence row={row} fold={false} />
        </Box>
      )}
      {row.owner_applicable && row.skipped && <OwnerApplyCell row={row} repairs={repairs} />}
    </Box>
  );
}

export function RepairsDetailsView({ repairs }: RepairsViewProps) {
  const books = usePageBooks(repairs.rows);
  return (
    <Box data-testid="repairs-details">
      {books.loading && (
        <Stack
          direction="row"
          spacing={1}
          sx={{ alignItems: 'center', px: 2, pt: 1 }}
          data-testid="repairs-details-books-loading"
        >
          <CircularProgress size={16} />
          <Typography variant="body2">Loading book details…</Typography>
        </Stack>
      )}
      {books.error && (
        <Alert
          severity="error"
          sx={{ mx: 1, mt: 1 }}
          data-testid="repairs-details-books-error"
          action={
            <Button color="inherit" size="small" onClick={books.retry}>
              Retry
            </Button>
          }
        >
          Book details could not be loaded: {books.error}
        </Alert>
      )}
      {repairs.rows.map((row) => (
        <DetailsCard key={row.row_id} row={row} repairs={repairs} books={books} />
      ))}
    </Box>
  );
}
