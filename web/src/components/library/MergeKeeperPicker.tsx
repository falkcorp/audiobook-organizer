// file: web/src/components/library/MergeKeeperPicker.tsx
// version: 1.0.0
// guid: 3a8f5c60-1b94-4e72-a6d3-7c0e9b2d4f18
// last-edited: 2026-10-22

import { useEffect, useState } from 'react';
import { Alert, Box, Chip, FormControlLabel, Radio, RadioGroup, Typography } from '@mui/material';
import * as api from '../../services/api';
import type { Audiobook } from '../../types';

export interface MergeKeeperPickerProps {
  books: Audiobook[];
  keepId: string;
  onChange: (id: string) => void;
}

/**
 * The choice of which book a "Merge as Versions" keeps. One radio per book, so
 * exactly one is chosen; each row says what the book is (title, author, how
 * many files it owns and where) and whether it is the one kept. The keep book
 * stays as it is; every other book is linked to it as an alternate version.
 */
export function MergeKeeperPicker({ books, keepId, onChange }: MergeKeeperPickerProps) {
  const fileCounts = useFileCounts(books);
  const keep = books.find((b) => b.id === keepId);
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
          const count = fileCounts[book.id];
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
                  {book.file_path && (
                    <Typography
                      variant="caption"
                      sx={{ color: 'text.secondary', display: 'block', wordBreak: 'break-all' }}
                    >
                      {book.file_path}
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
          {fileCounts[keep.id] != null ? ` (${fileCounts[keep.id]} files)` : ''}. The other {others}{' '}
          {others === 1 ? 'book is' : 'books are'} linked to it as alternate versions. Files stay in
          place.
        </Alert>
      )}
    </>
  );
}

/**
 * How many files each book owns: undefined while being read, null when the read
 * failed. The list rows do not carry a file count, so it is read per book (a
 * count-only request, no disk check) when the picker opens.
 */
function useFileCounts(books: Audiobook[]): Record<string, number | null | undefined> {
  const [counts, setCounts] = useState<Record<string, number | null | undefined>>({});
  const key = books.map((b) => b.id).join(',');
  useEffect(() => {
    const ac = new AbortController();
    setCounts({});
    for (const id of key.split(',').filter(Boolean)) {
      api
        .getBookFiles(id, { limit: 1, skipDiskCheck: true, signal: ac.signal })
        .then((r) => {
          if (!ac.signal.aborted) setCounts((prev) => ({ ...prev, [id]: r.count }));
        })
        .catch(() => {
          if (!ac.signal.aborted) setCounts((prev) => ({ ...prev, [id]: null }));
        });
    }
    return () => ac.abort();
  }, [key]);
  return counts;
}
