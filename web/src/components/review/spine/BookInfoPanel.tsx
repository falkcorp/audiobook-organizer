// file: web/src/components/review/spine/BookInfoPanel.tsx
// version: 1.0.0
// guid: 818c90ac-fac4-4fa5-a419-83bfae2d6889
// last-edited: 2026-10-07
//
// The book-info block every metadata review card shows on its LEFT: what the
// book is now (title, author, narrator, series, ASIN/ISBN), what it is made of
// (format, runtime, size, file count with an expandable file list) and where
// it lives (paths, with copy buttons via PathLinks). One component so the
// compact, two-column and candidates views cannot drift apart again -- the
// compact view used to show title, format and size only.
//
// The file list is NOT in the review list response: listing every file of a
// 200-row page is an N+1 the listing deliberately avoids. It is fetched on
// first expand (disk check off: names, sizes and durations are all it needs).

import { Box, Button, Chip, CircularProgress, Stack, Typography } from '@mui/material';
import { memo, useState } from 'react';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import ExpandLessIcon from '@mui/icons-material/ExpandLess';
import * as api from '../../../services/api';
import type { BookFile, CandidateBookInfo, PathAlias } from '../../../services/api';
import { PathLinks } from '../../common/PathLinks';
import type { PathVar } from '../../../utils/formatPath';
import { formatDuration, formatFileSize } from './rowState';
import { bookFileCount, bookRuntimeLabel } from './bookInfo';

function fileName(f: BookFile): string {
  const p = f.file_path || f.original_filename || '';
  const i = Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\'));
  return i >= 0 ? p.slice(i + 1) : p;
}

function FileList({ bookId, count }: { bookId: string; count?: number }) {
  const [open, setOpen] = useState(false);
  const [files, setFiles] = useState<BookFile[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const toggle = () => {
    const next = !open;
    setOpen(next);
    if (next && files === null && !loading) {
      setLoading(true);
      setError(null);
      api
        .getBookFiles(bookId, { skipDiskCheck: true })
        .then((r) => setFiles(r.files ?? []))
        .catch((err: unknown) =>
          setError(err instanceof Error ? err.message : 'Could not load the files')
        )
        .finally(() => setLoading(false));
    }
  };

  return (
    <Box>
      <Button
        size="small"
        variant="text"
        onClick={toggle}
        endIcon={open ? <ExpandLessIcon /> : <ExpandMoreIcon />}
        sx={{ px: 0.5, minWidth: 0 }}
        data-testid="book-files-toggle"
      >
        {count ? `${count} file${count === 1 ? '' : 's'}` : 'Files'}
      </Button>
      {open && (
        <Box data-testid="book-files-list" sx={{ pl: 1 }}>
          {loading && <CircularProgress size={14} />}
          {error && (
            <Typography variant="caption" color="error" sx={{ display: 'block' }}>
              {error}
            </Typography>
          )}
          {files?.length === 0 && (
            <Typography variant="caption" sx={{ display: 'block', color: 'text.secondary' }}>
              No file rows.
            </Typography>
          )}
          {files?.map((f) => (
            <Typography
              key={f.id}
              variant="caption"
              sx={{ display: 'block', wordBreak: 'break-all' }}
            >
              {fileName(f)}
              {f.file_size ? ` · ${formatFileSize(f.file_size)}` : ''}
              {f.duration ? ` · ${formatDuration(f.duration)}` : ''}
            </Typography>
          ))}
        </Box>
      )}
    </Box>
  );
}

export interface BookInfoPanelProps {
  book: CandidateBookInfo;
  pathAliases: PathAlias[];
  pathVars: PathVar[];
}

export const BookInfoPanel = memo(function BookInfoPanel({
  book,
  pathAliases,
  pathVars,
}: BookInfoPanelProps) {
  const runtime = bookRuntimeLabel(book);
  const files = bookFileCount(book);
  return (
    <Box sx={{ minWidth: 0 }} data-testid="book-info-panel">
      <Typography variant="body2" sx={{ fontWeight: 'bold' }}>
        {book.title}
      </Typography>
      {book.author && <Typography variant="body2">{book.author}</Typography>}
      {book.narrator && (
        <Typography variant="body2" sx={{ color: 'text.secondary' }}>
          Narrated by {book.narrator}
        </Typography>
      )}
      {book.series && (
        <Typography variant="body2">
          Series: {book.series}
          {book.series_position ? ` · Book ${book.series_position}` : ''}
        </Typography>
      )}
      {(book.asin || book.isbn) && (
        <Typography variant="caption" sx={{ display: 'block' }}>
          {book.asin ? `ASIN ${book.asin}` : ''}
          {book.asin && book.isbn ? ' · ' : ''}
          {book.isbn ? `ISBN ${book.isbn}` : ''}
        </Typography>
      )}
      <Stack direction="row" spacing={0.5} sx={{ flexWrap: 'wrap', alignItems: 'center', mt: 0.5 }}>
        {book.format && <Chip label={book.format} size="small" />}
        {runtime && <Typography variant="caption">{runtime}</Typography>}
        {book.file_size_bytes ? (
          <Typography variant="caption">· {formatFileSize(book.file_size_bytes)}</Typography>
        ) : null}
      </Stack>
      <FileList bookId={book.id} count={files} />
      <PathLinks path={book.file_path} aliases={pathAliases} vars={pathVars} />
      {book.itunes_path && (
        <Typography
          variant="caption"
          sx={{ color: 'info.main', display: 'block', wordBreak: 'break-all' }}
        >
          iTunes: {book.itunes_path}
        </Typography>
      )}
    </Box>
  );
});
