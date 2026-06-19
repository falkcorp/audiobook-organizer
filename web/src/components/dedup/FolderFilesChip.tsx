// file: web/src/components/dedup/FolderFilesChip.tsx
// version: 1.0.0
// guid: 4a1c8e92-6d35-4b70-9f28-1e7a5c3d2b69
// last-edited: 2026-06-19

// FolderFilesChip shows a small "Files" chip on a dedup candidate card. Clicking
// it opens a popover that lazily fetches the book's file list (getBookFiles) and
// shows each file's name, format, size and duration with a count header — so the
// user can tell a 197-file series from a single file without opening the drawer.

import { useState, useCallback, type MouseEvent } from 'react';
import {
  Chip, Popover, Box, Typography, List, ListItem, CircularProgress, Tooltip,
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
  const [files, setFiles] = useState<BookFile[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const open = useCallback(
    async (e: MouseEvent<HTMLElement>) => {
      // Don't let the click bubble to the row (row click = select).
      e.stopPropagation();
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
      <Tooltip title="Show files in this book">
        <Chip
          icon={<FolderOpenIcon />}
          label={count > 0 ? `${count} ${label}` : label}
          size="small"
          variant="outlined"
          clickable
          onClick={open}
        />
      </Tooltip>
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
              <Typography variant="body2" color="text.secondary">Loading files…</Typography>
            </Box>
          )}
          {error && (
            <Typography variant="body2" color="error.main">{error}</Typography>
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
                          sx={{ fontFamily: 'monospace', fontSize: '0.75rem', color: f.missing ? 'error.main' : 'text.primary' }}
                          noWrap
                        >
                          {basename(f.file_path)}
                        </Typography>
                      </Tooltip>
                      {meta && (
                        <Typography variant="caption" color="text.secondary">{meta}</Typography>
                      )}
                    </ListItem>
                  );
                })}
              </List>
            </>
          )}
        </Box>
      </Popover>
    </>
  );
}
