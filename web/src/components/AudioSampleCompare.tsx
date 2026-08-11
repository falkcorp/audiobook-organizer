// file: web/src/components/AudioSampleCompare.tsx
// version: 1.1.0
// guid: e5f6a7b8-c9d0-1e2f-3a4b-5c6d7e8f9a0b

import { useState, useRef, useCallback } from 'react';
import {
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Button,
  Stack,
  Typography,
  ToggleButtonGroup,
  ToggleButton,
  Box,
  Chip,
  LinearProgress,
  Tooltip,
} from '@mui/material';
import PlayArrowIcon from '@mui/icons-material/PlayArrow';
import PauseIcon from '@mui/icons-material/Pause';
import ShuffleIcon from '@mui/icons-material/Shuffle';

export interface SampleBook {
  id: string;
  title: string;
  authors?: string;
  filePath?: string;
  duration?: number; // seconds
}

interface Props {
  open: boolean;
  bookA: SampleBook;
  bookB: SampleBook;
  onClose: () => void;
  onKeep: (winnerId: string, loserId: string) => void;
}

type Position = 'start' | 'end' | 'random';

const CLIP_DURATION = 30; // seconds per sample

function buildSampleUrl(bookId: string, start: number): string {
  return `/api/v1/audiobooks/${bookId}/sample?start=${start}&duration=${CLIP_DURATION}`;
}

function resolveStart(pos: Position, duration: number | undefined, randomOffset: number): number {
  const dur = duration ?? 600; // assume 10min if unknown
  switch (pos) {
    case 'start':
      return 0;
    case 'end':
      return Math.max(0, dur - CLIP_DURATION);
    case 'random':
      return randomOffset;
  }
}

function randomOffset(duration: number | undefined): number {
  const dur = duration ?? 600;
  const max = Math.max(0, dur - CLIP_DURATION * 2);
  // Pick from the middle 60% to avoid the very start/end
  const low = Math.floor(max * 0.2);
  const high = Math.floor(max * 0.8);
  return low + Math.floor(Math.random() * Math.max(1, high - low));
}

interface PlayerProps {
  book: SampleBook;
  src: string;
  label: string;
  selected: boolean;
  onSelect: () => void;
}

function SamplePlayer({ book, src, label, selected, onSelect }: PlayerProps) {
  const audioRef = useRef<HTMLAudioElement>(null);
  const [playing, setPlaying] = useState(false);
  const [progress, setProgress] = useState(0);
  const [loading, setLoading] = useState(false);

  const toggle = useCallback(() => {
    const el = audioRef.current;
    if (!el) return;
    if (playing) {
      el.pause();
    } else {
      setLoading(true);
      el.play().catch(() => setLoading(false));
    }
  }, [playing]);

  return (
    <Box
      onClick={onSelect}
      sx={[{
        flex: 1,
        border: 2,
        borderRadius: 2,
        p: 2,
        cursor: 'pointer',
        transition: 'border-color 0.15s',
        '&:hover': { borderColor: 'primary.light' }
      }, selected ? {
        borderColor: 'primary.main'
      } : {
        borderColor: 'divider'
      }, selected ? {
        bgcolor: 'action.selected'
      } : {
        bgcolor: 'background.paper'
      }]}
    >
      <Stack spacing={1}>
        <Stack direction="row" alignItems="center" justifyContent="space-between">
          <Typography variant="caption" color="text.secondary" fontWeight={600}>
            {label}
          </Typography>
          {selected && <Chip label="Selected" size="small" color="primary" />}
        </Stack>

        <Typography variant="body2" fontWeight={600} noWrap title={book.title}>
          {book.title}
        </Typography>
        {book.authors && (
          <Typography variant="caption" color="text.secondary" noWrap>
            {book.authors}
          </Typography>
        )}
        {book.filePath && (
          <Typography
            variant="caption"
            color="text.disabled"
            noWrap
            title={book.filePath}
            sx={{ fontSize: '0.65rem' }}
          >
            {book.filePath}
          </Typography>
        )}

        <audio
          ref={audioRef}
          src={src}
          preload="none"
          onPlay={() => { setPlaying(true); setLoading(false); }}
          onPause={() => setPlaying(false)}
          onEnded={() => { setPlaying(false); setProgress(0); }}
          onTimeUpdate={() => {
            const el = audioRef.current;
            if (el && el.duration) setProgress(el.currentTime / el.duration * 100);
          }}
          onWaiting={() => setLoading(true)}
          onCanPlay={() => setLoading(false)}
        />

        {loading && <LinearProgress sx={{ borderRadius: 1 }} />}
        {!loading && <LinearProgress variant="determinate" value={progress} sx={{ borderRadius: 1 }} />}

        <Button
          size="small"
          variant={playing ? 'outlined' : 'contained'}
          startIcon={playing ? <PauseIcon /> : <PlayArrowIcon />}
          onClick={(e) => { e.stopPropagation(); toggle(); }}
          fullWidth
        >
          {playing ? 'Pause' : 'Play'}
        </Button>
      </Stack>
    </Box>
  );
}

export function AudioSampleCompare({ open, bookA, bookB, onClose, onKeep }: Props) {
  const [position, setPosition] = useState<Position>('start');
  const [randomSeed, setRandomSeed] = useState(() =>
    randomOffset(bookA.duration ?? bookB.duration)
  );
  const [selected, setSelected] = useState<string | null>(null);

  const reroll = () => {
    setRandomSeed(randomOffset(bookA.duration ?? bookB.duration));
    setPosition('random');
  };

  const startA = resolveStart(position, bookA.duration, randomSeed);
  const startB = resolveStart(position, bookB.duration, randomSeed); // same timestamp

  const srcA = buildSampleUrl(bookA.id, startA);
  const srcB = buildSampleUrl(bookB.id, startB);

  const handleKeep = () => {
    if (!selected) return;
    const loserId = selected === bookA.id ? bookB.id : bookA.id;
    onKeep(selected, loserId);
  };

  return (
    <Dialog open={open} onClose={onClose} maxWidth="md" fullWidth>
      <DialogTitle>Compare Audio Samples</DialogTitle>
      <DialogContent>
        <Stack spacing={2}>
          {/* Position selector */}
          <Stack direction="row" alignItems="center" spacing={1}>
            <Typography variant="body2" color="text.secondary">
              Sample position:
            </Typography>
            <ToggleButtonGroup
              value={position}
              exclusive
              onChange={(_, v) => v && setPosition(v)}
              size="small"
            >
              <ToggleButton value="start">First {CLIP_DURATION}s</ToggleButton>
              <ToggleButton value="end">Last {CLIP_DURATION}s</ToggleButton>
              <ToggleButton value="random">Random</ToggleButton>
            </ToggleButtonGroup>
            <Tooltip title="Pick a new random position">
              <Button size="small" startIcon={<ShuffleIcon />} onClick={reroll} variant="outlined">
                Reroll
              </Button>
            </Tooltip>
            {position === 'random' && (
              <Typography variant="caption" color="text.secondary">
                @{startA}s
              </Typography>
            )}
          </Stack>

          <Typography variant="caption" color="text.secondary">
            Click a player to select it as the version to keep, then confirm below.
          </Typography>

          {/* Side-by-side players */}
          <Stack direction="row" spacing={2} sx={{ minHeight: 220 }}>
            <SamplePlayer
              book={bookA}
              src={srcA}
              label="Version A"
              selected={selected === bookA.id}
              onSelect={() => setSelected(bookA.id)}
            />
            <SamplePlayer
              book={bookB}
              src={srcB}
              label="Version B"
              selected={selected === bookB.id}
              onSelect={() => setSelected(bookB.id)}
            />
          </Stack>
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          variant="contained"
          color="primary"
          disabled={!selected}
          onClick={handleKeep}
        >
          Keep {selected === bookA.id ? 'Version A' : selected === bookB.id ? 'Version B' : '…'}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
