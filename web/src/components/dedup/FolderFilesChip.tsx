// file: web/src/components/dedup/FolderFilesChip.tsx
// version: 1.1.1
// guid: 4a1c8e92-6d35-4b70-9f28-1e7a5c3d2b69
// last-edited: 2026-09-01

// FolderFilesChip shows a small "Files" chip on a dedup candidate card. Clicking
// it opens a popover that lazily fetches the book's file list (getBookFiles) and
// shows each file's name, format, size and duration with a count header — so the
// user can tell a 197-file series from a single file without opening the drawer.

import { useState, useCallback, type MouseEvent } from 'react';
import {
  Chip,
  Popover,
  Box,
  Typography,
  List,
  ListItem,
  CircularProgress,
  Tooltip,
} from '@mui/material';
import FolderOpenIcon from '@mui/icons-material/FolderOpen';
import { getBookFiles, type BookFile } from '../../services/api';

interface FolderFilesChipProps {
  bookId: string;
  label?: string;
}

function basename(p: string): string {
  if (!p) return '';
  const parts = p.split('/');
  return parts[parts.length - 1] || p;
}

function humanSize(bytes?: number): string {
  if (!bytes || bytes <= 0) return '';
  const units = ['B', 'KB', 'MB', 'GB'];
  let v = bytes;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
}

function humanDuration(sec?: number): string {
  if (!sec || sec <= 0) return '';
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m`;
  return `${Math.floor(sec)}s`;
}

export function FolderFilesChip({ bookId, label = 'Files' }: FolderFilesChipProps) {
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null);
  // Gated on "has EVER been opened", not "is open now". Gating on the latter
  // would unmount the Popover the instant close() nulls the anchor, cutting
  // off MUI's exit transition -- the popover would snap shut instead of
  // fading. The saving is identical either way: the rows nobody clicks still
  // build nothing. (This repo has form here; see the MuiMenu exit:0 note in
  // theme.ts.)
  const [everOpened, setEverOpened] = useState(false);
  const [files, setFiles] = useState<BookFile[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const open = useCallback(
    async (e: MouseEvent<HTMLElement>) => {
      // Don't let the click bubble to the row (row click = select).
      e.stopPropagation();
      setEverOpened(true);
      setAnchorEl(e.currentTarget);
      if (files || loading) return; // lazy-load once
      setLoading(true);
      setError(null);
      try {
        const ctrl = new AbortController();
        const res = await getBookFiles(bookId, { signal: ctrl.signal });
        setFiles(res.files || []);
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Failed to load files');
      } finally {
        setLoading(false);
      }
    },
    [bookId, files, loading]
  );

  const close = useCallback(() => setAnchorEl(null), []);

  const count = files?.length ?? 0;

  return (
    <>
      {/* A plain `title` rather than an MUI <Tooltip>: this chip renders twice
          per dupes row and an MUI Tooltip costs ~85 ms per instance per 100
          rows -- 45% of the lane's blocked main-thread time came from the four
          Tooltips on a row. See the note in PathLinks.tsx. The chip already
          reads "Files"/"N Files", so the hint is supplementary. */}
      <Chip
        icon={<FolderOpenIcon />}
        label={count > 0 ? `${count} ${label}` : label}
        title="Show files in this book"
        size="small"
        variant="outlined"
        clickable
        onClick={open}
      />
      {/*
        The Popover is not built until the chip is first clicked. MUI's Modal does
        early-return null when closed, but only AFTER useDefaultProps (a theme
        lookup), useModal (refs, callbacks, state) and emotion's processing of
        the styled PopoverRoot -- so a closed Popover is not free. This chip
        renders twice per dupes row, and ablating the closed Popover at the
        100-row page cap was measured at 40 ms of 763 ms (5%) of the lane's
        blocked main-thread time. Small, but it buys nothing at all until the
        user clicks.
      */}
      {everOpened && (
        <Popover
          open={Boolean(anchorEl)}
          anchorEl={anchorEl}
          onClose={close}
          anchorOrigin={{ vertical: 'bottom', horizontal: 'left' }}
          onClick={(e) => e.stopPropagation()}
        >
          <Box sx={{ p: 1.5, minWidth: 280, maxWidth: 560 }}>
            {loading && (
              <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, py: 1 }}>
                <CircularProgress size={16} />
                <Typography
                  variant="body2"
                  sx={{
                    color: 'text.secondary',
                  }}
                >
                  Loading files…
                </Typography>
              </Box>
            )}
            {error && (
              <Typography
                variant="body2"
                sx={{
                  color: 'error.main',
                }}
              >
                {error}
              </Typography>
            )}
            {!loading && !error && files && (
              <>
                <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
                  {count} {count === 1 ? 'file' : 'files'}
                </Typography>
                <List dense disablePadding sx={{ maxHeight: 320, overflow: 'auto' }}>
                  {files.map((f) => {
                    const meta = [f.format, humanSize(f.file_size), humanDuration(f.duration)]
                      .filter(Boolean)
                      .join(' · ');
                    return (
                      <ListItem key={f.id} disableGutters sx={{ display: 'block', py: 0.25 }}>
                        <Tooltip title={f.file_path} placement="bottom-start">
                          <Typography
                            variant="body2"
                            sx={{
                              fontFamily: 'monospace',
                              fontSize: '0.75rem',
                              color: f.missing ? 'error.main' : 'text.primary',
                            }}
                            noWrap
                          >
                            {basename(f.file_path)}
                          </Typography>
                        </Tooltip>
                        {meta && (
                          <Typography
                            variant="caption"
                            sx={{
                              color: 'text.secondary',
                            }}
                          >
                            {meta}
                          </Typography>
                        )}
                      </ListItem>
                    );
                  })}
                </List>
              </>
            )}
          </Box>
        </Popover>
      )}
    </>
  );
}
