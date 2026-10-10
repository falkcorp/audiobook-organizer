// file: web/src/components/review/compare/AudioSamplePair.tsx
// version: 1.1.0
// guid: f5e6a7b8-c9d0-1234-efab-fe5678901234
// last-edited: 2026-10-10

// AudioSamplePair renders a compact audio preview pair for two books inside
// the CandidateCompareDrawer. Wraps the existing AudioSampleCompare dialog
// but surfaced inline as a button that opens the comparison modal.

import { useState } from 'react';
import { Button } from '@mui/material';
import HeadphonesIcon from '@mui/icons-material/Headphones';
import { AudioSampleCompare, type SampleBook } from '../../AudioSampleCompare';
import type { DedupBookDetail } from '../../../services/api';

interface AudioSamplePairProps {
  bookA: DedupBookDetail;
  bookB: DedupBookDetail;
  /** Called when the user picks a winner in the audio comparison. */
  onKeep?: (winnerId: string, loserId: string) => void;
}

function toSampleBook(book: DedupBookDetail): SampleBook {
  return {
    id: book.id,
    title: book.title,
    authors: book.author_name,
    filePath: book.files?.[0]?.file_path ?? book.file_path,
    duration: book.duration,
  };
}

export function AudioSamplePair({ bookA, bookB, onKeep }: AudioSamplePairProps) {
  const [open, setOpen] = useState(false);

  return (
    <>
      <Button
        size="small"
        variant="outlined"
        startIcon={<HeadphonesIcon />}
        onClick={() => setOpen(true)}
        data-testid="audio-sample-pair-btn"
      >
        Compare Audio
      </Button>
      {open && (
        <AudioSampleCompare
          open={open}
          bookA={toSampleBook(bookA)}
          bookB={toSampleBook(bookB)}
          onClose={() => setOpen(false)}
          onKeep={(winnerId, loserId) => {
            setOpen(false);
            onKeep?.(winnerId, loserId);
          }}
        />
      )}
    </>
  );
}
