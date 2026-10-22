// file: web/src/components/library/MergeKeeperPicker.tsx
// version: 1.1.0
// guid: 3a8f5c60-1b94-4e72-a6d3-7c0e9b2d4f18
// last-edited: 2026-10-22

import { useEffect, useState } from 'react';
import { Alert, Box, Chip, FormControlLabel, Radio, RadioGroup, Typography } from '@mui/material';
import * as api from '../../services/api';
import type { Audiobook } from '../../types';
import { commonFolder } from '../../utils/commonFolder';

export interface MergeKeeperPickerProps {
  books: Audiobook[];
  keepId: string;
  onChange: (id: string) => void;
}

/**
 * The choice of which book a "Merge as Versions" keeps. One radio per book, so
 * exactly one is chosen; each row says what the book is (title, author, how
 * many files it owns and the folder they share) and whether it is the one kept. The keep book
 * stays as it is; every other book is linked to it as an alternate version.
 */
export function MergeKeeperPicker({ books, keepId, onChange }: MergeKeeperPickerProps) {
  const info = useBookFileInfo(books);
  const keep = books.find((b) => b.id === keepId);
  const keepInfo = keep ? info[keep.id] : undefined;
  const keepCount = keepInfo ? keepInfo.count : null;
  const others = books.filter((b) => b.id !== keepId).length;

  return (
    <>
      <RadioGroup
        value={keepId}
        onChange={(e) => onChange(e.target.value)}
        aria-label="Book to keep"
        data-testid="merge-keeper-group"
      >
        {books.map((book) => {
          const kept = book.id === keepId;
          const bookInfo = info[book.id];
          const count = bookInfo === undefined || bookInfo === null ? bookInfo : bookInfo.count;
          // The folder every file of the book shares (the library shows books
          // by folder); the book's own file_path only when it has no file rows.
          const folder = bookInfo?.folder || '';
          const location = folder
            ? {
                label: bookInfo?.partial ? 'Folder (of the first files)' : 'Folder',
                value: folder,
              }
            : book.file_path
              ? { label: 'Path', value: book.file_path }
              : null;
          return (
            <FormControlLabel
              key={book.id}
              value={book.id}
              control={<Radio slotProps={{ input: { 'aria-label': `Keep ${book.title}` } }} />}
              sx={{ alignItems: 'flex-start', mb: 1 }}
              label={
                <Box data-testid={`merge-row-${book.id}`}>
                  <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap' }}>
                    <Typography variant="body2">
                      <strong>{book.title}</strong>
                      {book.author ? ` by ${book.author}` : ''}
                    </Typography>
                    <Chip
                      size="small"
                      color={kept ? 'success' : 'default'}
                      variant={kept ? 'filled' : 'outlined'}
                      label={kept ? 'Kept' : 'Linked as a version'}
                      data-testid={kept ? 'merge-kept-chip' : 'merge-linked-chip'}
                    />
                  </Box>
                  <Typography variant="caption" sx={{ color: 'text.secondary', display: 'block' }}>
                    {count === undefined
                      ? 'Counting files...'
                      : count === null
                        ? 'File count unavailable'
                        : `${count} ${count === 1 ? 'file' : 'files'}`}
                    {book.format ? ` · ${book.format}` : ''}
                  </Typography>
                  {location && (
                    <Typography
                      variant="caption"
                      sx={{ color: 'text.secondary', display: 'block', wordBreak: 'break-all' }}
                      data-testid={`merge-location-${book.id}`}
                    >
                      {location.label}: <span>{location.value}</span>
                    </Typography>
                  )}
                </Box>
              }
            />
          );
        })}
      </RadioGroup>
      {keep && (
        <Alert severity="info" sx={{ mt: 1 }} data-testid="merge-keeper-summary">
          Keeping <strong>{keep.title}</strong>
          {keepCount != null ? ` (${keepCount} files)` : ''}. The other {others}{' '}
          {others === 1 ? 'book is' : 'books are'} linked to it as alternate versions. Files stay in
          place.
        </Alert>
      )}
    </>
  );
}

/** What the picker shows of a book's files: how many, and the folder they share. */
interface BookFileInfo {
  count: number;
  folder: string;
  /** more files exist than were read, so the folder is the common one of a subset */
  partial: boolean;
}

/** Files read per book to find their common folder. */
const FILES_READ_LIMIT = 500;

/**
 * The file count and shared folder of each book: undefined while being read,
 * null when the read failed. The list rows carry neither, so they are read per
 * book (no disk check) when the picker opens.
 */
function useBookFileInfo(books: Audiobook[]): Record<string, BookFileInfo | null | undefined> {
  const [info, setInfo] = useState<Record<string, BookFileInfo | null | undefined>>({});
  const key = books.map((b) => b.id).join(',');
  useEffect(() => {
    const ac = new AbortController();
    setInfo({});
    for (const id of key.split(',').filter(Boolean)) {
      api
        .getBookFiles(id, { limit: FILES_READ_LIMIT, skipDiskCheck: true, signal: ac.signal })
        .then((r) => {
          if (ac.signal.aborted) return;
          const folder = commonFolder(r.files.map((f) => f.file_path));
          setInfo((prev) => ({
            ...prev,
            [id]: { count: r.count, folder, partial: r.count > r.files.length && folder !== '' },
          }));
        })
        .catch(() => {
          if (!ac.signal.aborted) setInfo((prev) => ({ ...prev, [id]: null }));
        });
    }
    return () => ac.abort();
  }, [key]);
  return info;
}
