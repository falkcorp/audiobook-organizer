// file: web/src/components/bookdetail/WhisperIntroPanel.tsx
// version: 1.0.0
// guid: b2c3d4e5-f6a7-8901-bcde-f01234567890
// last-edited: 2026-06-26

import { useState } from 'react';
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Box,
  Chip,
  Tooltip,
  Typography,
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore.js';
import MicIcon from '@mui/icons-material/Mic.js';
import type { Book } from '../../services/api';

interface Props {
  book: Book;
}

export function WhisperIntroPanel({ book }: Props) {
  const [expanded, setExpanded] = useState(false);

  if (!book.intro_transcription && !book.intro_transcribed_at) return null;

  const hasExtracted = book.transcribed_title || book.transcribed_author || book.transcribed_narrator;
  const transcribedAt = book.intro_transcribed_at
    ? new Date(book.intro_transcribed_at).toLocaleString()
    : null;

  const isShort = (book.intro_transcription?.length ?? 0) <= 20;

  return (
    <Accordion
      expanded={expanded}
      onChange={(_, v) => setExpanded(v)}
      disableGutters
      sx={{ mb: 3, '&:before': { display: 'none' }, borderRadius: 1, border: '1px solid', borderColor: 'divider' }}
      elevation={0}
    >
      <AccordionSummary expandIcon={<ExpandMoreIcon />}>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
          <MicIcon fontSize="small" color="action" />
          <Typography variant="subtitle1" fontWeight={600}>
            Whisper Intro
          </Typography>
          {isShort && (
            <Tooltip title="Transcript is very short — may only have caught the Audible bumper">
              <Chip label="short" size="small" color="warning" variant="outlined" />
            </Tooltip>
          )}
          {!isShort && hasExtracted && (
            <Chip label="parsed" size="small" color="success" variant="outlined" />
          )}
          {transcribedAt && (
            <Typography variant="caption" color="text.secondary" sx={{ ml: 'auto', pr: 1 }}>
              {transcribedAt}
            </Typography>
          )}
        </Box>
      </AccordionSummary>

      <AccordionDetails>
        {book.intro_transcription && (
          <Box sx={{ mb: hasExtracted ? 2 : 0 }}>
            <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 0.5, textTransform: 'uppercase', letterSpacing: 0.5 }}>
              Raw Transcript
            </Typography>
            <Typography
              variant="body2"
              sx={{
                fontStyle: 'italic',
                color: isShort ? 'warning.main' : 'text.primary',
                bgcolor: 'action.hover',
                borderRadius: 1,
                p: 1.5,
                whiteSpace: 'pre-wrap',
              }}
            >
              {book.intro_transcription}
            </Typography>
          </Box>
        )}

        {hasExtracted && (
          <Box sx={{ display: 'flex', flexDirection: 'column', gap: 1 }}>
            <Typography variant="caption" color="text.secondary" sx={{ textTransform: 'uppercase', letterSpacing: 0.5 }}>
              Extracted
            </Typography>
            {[
              { label: 'Title', value: book.transcribed_title },
              { label: 'Author', value: book.transcribed_author },
              { label: 'Narrator', value: book.transcribed_narrator },
            ].map(({ label, value }) => value && (
              <Box key={label} sx={{ display: 'flex', gap: 1.5, alignItems: 'baseline' }}>
                <Typography variant="body2" color="text.secondary" sx={{ width: 64, flexShrink: 0 }}>
                  {label}
                </Typography>
                <Typography variant="body2">{value}</Typography>
              </Box>
            ))}
          </Box>
        )}
      </AccordionDetails>
    </Accordion>
  );
}
