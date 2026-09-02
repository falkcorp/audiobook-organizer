// file: web/src/pages/AuthorDetail.tsx
// version: 1.0.0
// guid: 98408901-8ba3-414c-9436-84b79b34ceaf
// last-edited: 2026-09-02

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Alert,
  Box,
  Breadcrumbs,
  Button,
  Chip,
  CircularProgress,
  Link as MuiLink,
  Paper,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from '@mui/material';
import ArrowBackIcon from '@mui/icons-material/ArrowBack';
import RefreshIcon from '@mui/icons-material/Refresh';
import * as api from '../services/api';

/**
 * AuthorDetail is the drill-down for a single author, reachable from the
 * Authors list and from the Author field on any book.
 *
 * Before this page existed the app had no addressable author: `/authors` was a
 * list that downloaded every row and filtered client-side, and a book's author
 * was rendered as plain text. Clicking an author did nothing, because there was
 * nothing to click.
 *
 * The author's own row and its books are two separate reads on purpose. The row
 * (name, counts, aliases) comes from the cached whole-library aggregate, so the
 * counts shown here are the same numbers the Authors list shows. The books come
 * from the junction-aware getter, which includes titles this author is credited
 * on as a co-author — so `book_count` and the row count below can legitimately
 * disagree, and the page says so rather than hiding one of them.
 */
export default function AuthorDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();

  const authorId = useMemo(() => {
    const n = Number(id);
    return Number.isInteger(n) && n > 0 ? n : null;
  }, [id]);

  const [author, setAuthor] = useState<api.AuthorWithCount | null>(null);
  const [books, setBooks] = useState<api.Book[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [booksError, setBooksError] = useState<string | null>(null);

  const load = useCallback(async () => {
    if (authorId === null) {
      setError('Invalid author id');
      setLoading(false);
      return;
    }
    setLoading(true);
    setError(null);
    setBooksError(null);
    // The two reads are settled independently: a failure to list this author's
    // books must not blank out the author's own identity, and vice versa. A
    // combined await would have surfaced whichever failed first as "the" error.
    const [authorResult, booksResult] = await Promise.allSettled([
      api.getAuthor(authorId),
      api.getAuthorBooks(authorId),
    ]);

    if (authorResult.status === 'fulfilled') {
      setAuthor(authorResult.value);
    } else {
      setError(
        authorResult.reason instanceof Error
          ? authorResult.reason.message
          : 'Failed to load author'
      );
    }
    if (booksResult.status === 'fulfilled') {
      setBooks(booksResult.value);
    } else {
      setBooksError(
        booksResult.reason instanceof Error
          ? booksResult.reason.message
          : 'Failed to load this author’s books'
      );
    }
    setLoading(false);
  }, [authorId]);

  useEffect(() => {
    void load();
  }, [load]);

  if (loading) {
    return (
      <Box sx={{ display: 'flex', justifyContent: 'center', p: 6 }}>
        <CircularProgress />
      </Box>
    );
  }

  return (
    <Box sx={{ p: 3 }}>
      <Breadcrumbs sx={{ mb: 2 }}>
        <MuiLink
          component="button"
          underline="hover"
          color="inherit"
          onClick={() => navigate('/authors')}
        >
          Authors
        </MuiLink>
        <Typography color="text.primary">{author?.name ?? id}</Typography>
      </Breadcrumbs>

      {error && (
        <Alert
          severity="error"
          sx={{ mb: 2 }}
          action={
            <Button color="inherit" size="small" onClick={() => void load()}>
              Retry
            </Button>
          }
        >
          {error}
        </Alert>
      )}

      <Stack direction="row" spacing={2} sx={{ mb: 3, alignItems: 'center' }}>
        <Button startIcon={<ArrowBackIcon />} onClick={() => navigate('/authors')} size="small">
          Back
        </Button>
        <Typography variant="h5" sx={{ flexGrow: 1 }}>
          {author?.name ?? `Author ${id}`}
        </Typography>
        <Button startIcon={<RefreshIcon />} onClick={() => void load()} size="small">
          Refresh
        </Button>
      </Stack>

      {author && (
        <Stack direction="row" spacing={1} sx={{ mb: 3, flexWrap: 'wrap', gap: 1 }}>
          <Chip label={`${author.book_count} book${author.book_count === 1 ? '' : 's'}`} />
          <Chip label={`${author.file_count} file${author.file_count === 1 ? '' : 's'}`} />
          {(author.aliases ?? []).map((alias) => (
            <Chip
              key={alias.id}
              size="small"
              variant="outlined"
              label={`${alias.alias_type}: ${alias.alias_name}`}
            />
          ))}
        </Stack>
      )}

      {booksError && (
        <Alert
          severity="warning"
          sx={{ mb: 2 }}
          action={
            <Button color="inherit" size="small" onClick={() => void load()}>
              Retry
            </Button>
          }
        >
          {booksError}
        </Alert>
      )}

      {/* The credited count can exceed book_count: this getter is junction-aware,
          so it includes titles where this author is a co-author rather than the
          book's primary author. Naming both numbers is the honest presentation. */}
      <Typography variant="subtitle2" sx={{ mb: 1, color: 'text.secondary' }}>
        {books.length} credited {books.length === 1 ? 'title' : 'titles'}
      </Typography>

      {!booksError && books.length === 0 ? (
        <Alert severity="info">This author has no books in the library.</Alert>
      ) : (
        <TableContainer component={Paper} sx={{ overflowX: 'auto' }}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Title</TableCell>
                <TableCell>Series</TableCell>
                <TableCell>Narrator</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {books.map((book) => (
                <TableRow
                  key={book.id}
                  hover
                  sx={{ cursor: 'pointer' }}
                  onClick={() => navigate(`/library/${book.id}`)}
                >
                  <TableCell>{book.title || '—'}</TableCell>
                  <TableCell>
                    {book.series_name
                      ? `${book.series_name}${book.series_position ? ` #${book.series_position}` : ''}`
                      : '—'}
                  </TableCell>
                  <TableCell>{book.narrator || '—'}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Box>
  );
}
