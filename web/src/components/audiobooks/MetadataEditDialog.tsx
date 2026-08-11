// file: web/src/components/audiobooks/MetadataEditDialog.tsx
// version: 2.2.0
// guid: 4a5b6c7d-8e9f-0a1b-2c3d-4e5f6a7b8c9d

import React, { useState, useEffect, useCallback } from 'react';
import {
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Button,
  TextField,
  Box,
  Alert,
  Typography,
  IconButton,
  Tooltip,
  CircularProgress,
  Stack,
} from '@mui/material';
import LockIcon from '@mui/icons-material/Lock';
import LockOpenIcon from '@mui/icons-material/LockOpen';
import type { Audiobook } from '../../types';
import type { MetadataFieldStates, MetadataFieldStateEntry } from '../../services/api';
import * as api from '../../services/api';

// Maps Audiobook field names to their backend field-state keys
const FIELD_STATE_KEYS: Record<string, string> = {
  title: 'title',
  author: 'author_name',
  narrator: 'narrator',
  series: 'series_name',
  series_number: 'series_position',
  genre: 'genre',
  year: 'audiobook_release_year',
  language: 'language',
  publisher: 'publisher',
  isbn10: 'isbn10',
  isbn13: 'isbn13',
  description: 'description',
};

function formatValue(val: unknown): string {
  if (val === undefined || val === null || val === '') return '';
  return String(val);
}

interface MetadataEditDialogProps {
  open: boolean;
  audiobook: Audiobook | null;
  onClose: () => void;
  onSave: (audiobook: Audiobook, dirtyFields: Set<string>) => Promise<void>;
}

export const MetadataEditDialog: React.FC<MetadataEditDialogProps> = ({
  open,
  audiobook,
  onClose,
  onSave,
}) => {
  const [formData, setFormData] = useState<Partial<Audiobook>>({});
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [yearInput, setYearInput] = useState('');
  const [yearError, setYearError] = useState<string | null>(null);
  const [fieldStates, setFieldStates] = useState<MetadataFieldStates>({});
  const [loadingStates, setLoadingStates] = useState(false);
  // Track which fields were manually edited this session
  const [dirtyFields, setDirtyFields] = useState<Set<string>>(new Set());
  // Track local lock overrides (user toggled lock in this session)
  const [lockOverrides, setLockOverrides] = useState<Record<string, boolean>>({});

  const loadFieldStates = useCallback(async (bookId: string) => {
    setLoadingStates(true);
    try {
      const states = await api.getAudiobookFieldStates(bookId);
      setFieldStates(states);
    } catch {
      setFieldStates({});
    } finally {
      setLoadingStates(false);
    }
  }, []);

  useEffect(() => {
    if (audiobook) {
      setFormData(audiobook);
      setYearInput(
        typeof audiobook.year === 'number' ? String(audiobook.year) : ''
      );
      setYearError(null);
      setDirtyFields(new Set());
      setLockOverrides({});
    }
  }, [audiobook]);

  useEffect(() => {
    if (open && audiobook?.id) {
      loadFieldStates(audiobook.id);
    }
    if (!open) {
      setFieldStates({});
      setDirtyFields(new Set());
      setLockOverrides({});
    }
  }, [open, audiobook?.id, loadFieldStates]);

  const markDirty = (field: string) => {
    setDirtyFields((prev) => new Set(prev).add(field));
  };

  const handleChange = (field: keyof Audiobook, value: string | number) => {
    setFormData((prev) => ({ ...prev, [field]: value }));
    markDirty(field);
  };

  const handleYearChange = (value: string) => {
    setYearInput(value);
    markDirty('year');
    if (value.trim() === '') {
      setYearError(null);
      setFormData((prev) => ({ ...prev, year: undefined }));
      return;
    }
    const parsed = Number(value);
    if (!Number.isFinite(parsed)) {
      setYearError('Year must be a number');
      return;
    }
    setYearError(null);
    setFormData((prev) => ({ ...prev, year: parsed }));
  };

  const handleSave = async () => {
    if (!audiobook) return;
    setSaving(true);
    setError(null);
    try {
      if (yearError) {
        setError(yearError);
        setSaving(false);
        return;
      }
      await onSave({ ...audiobook, ...formData }, dirtyFields);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save metadata');
    } finally {
      setSaving(false);
    }
  };

  const getFieldState = (formField: string): MetadataFieldStateEntry | undefined => {
    const stateKey = FIELD_STATE_KEYS[formField];
    return stateKey ? fieldStates[stateKey] : undefined;
  };

  const isFieldLocked = (formField: string): boolean => {
    // Local override takes precedence
    if (formField in lockOverrides) return lockOverrides[formField];
    // Dirty fields are auto-locked (Plex behavior)
    if (dirtyFields.has(formField)) return true;
    // Backend state
    const entry = getFieldState(formField);
    return entry?.override_locked ?? false;
  };

  const toggleLock = (formField: string) => {
    const currentlyLocked = isFieldLocked(formField);
    setLockOverrides((prev) => ({ ...prev, [formField]: !currentlyLocked }));
  };

  const renderField = (
    field: keyof Audiobook,
    label: string,
    options?: {
      multiline?: boolean;
      rows?: number;
      type?: string;
      width?: string;
    }
  ) => {
    const { multiline, rows, type, width } = options || {};
    const currentVal = field === 'year' ? yearInput : String(formData[field] ?? '');
    const entry = getFieldState(field);
    const locked = isFieldLocked(field);
    const isDirty = dirtyFields.has(field);
    const fetchedStr = entry ? formatValue(entry.fetched_value) : '';
    const showFetched = fetchedStr !== '' && fetchedStr !== currentVal;

    // Source label
    let sourceLabel = '';
    if (isDirty) {
      sourceLabel = 'Manual override';
    } else if (entry) {
      if (entry.override_value !== undefined && entry.override_value !== null) {
        sourceLabel = 'Manual override';
      } else if (entry.fetched_value !== undefined && entry.fetched_value !== null) {
        sourceLabel = 'Fetched';
      } else {
        sourceLabel = 'File tags';
      }
    }

    return (
      <Box
        sx={{
          flex: width || '1 1 100%',
          minWidth: 0,
          position: 'relative',
        }}
      >
        <Box sx={{ display: 'flex', alignItems: 'flex-start', gap: 0 }}>
          <Tooltip
            title={
              locked
                ? 'Locked — will not be overwritten by metadata fetches. Click to unlock.'
                : 'Unlocked — may be updated by metadata fetches. Click to lock.'
            }
          >
            <IconButton
              size="small"
              onClick={() => toggleLock(field)}
              aria-label={`${locked ? 'Unlock' : 'Lock'} ${label}`}
              sx={[{
                mt: '10px',
                mr: 0.5,
                flexShrink: 0,
                width: 32,
                height: 32,
                border: '1px solid',
                borderRadius: 1
              }, locked ? {
                borderColor: 'warning.main'
              } : {
                borderColor: 'divider'
              }, locked ? {
                bgcolor: 'rgba(237, 108, 2, 0.08)'
              } : {
                bgcolor: 'transparent'
              }, locked ? {
                '&:hover': {
                  bgcolor: 'rgba(237, 108, 2, 0.16)'
                }
              } : {
                '&:hover': {
                  bgcolor: 'action.hover'
                }
              }]}
            >
              {locked ? (
                <LockIcon sx={{ fontSize: 16, color: 'warning.main' }} />
              ) : (
                <LockOpenIcon sx={{ fontSize: 16, color: 'text.disabled' }} />
              )}
            </IconButton>
          </Tooltip>
          <Box sx={{ flexGrow: 1 }}>
            <TextField
              fullWidth
              label={label}
              value={field === 'year' ? yearInput : (formData[field] ?? '')}
              onChange={(e) => {
                if (field === 'year') {
                  handleYearChange(e.target.value);
                } else if (type === 'number') {
                  handleChange(field, parseInt(e.target.value) || 0);
                } else {
                  handleChange(field, e.target.value);
                }
              }}
              multiline={multiline}
              rows={rows}
              type={field === 'year' ? 'text' : type}
              inputMode={field === 'year' ? 'numeric' : undefined}
              error={field === 'year' ? Boolean(yearError) : undefined}
              helperText={field === 'year' ? (yearError || undefined) : undefined}
              required={field === 'title'}
              size="small"
              sx={[isDirty ? {
                '& .MuiOutlinedInput-root': {
                  bgcolor: 'rgba(237, 108, 2, 0.04)'
                }
              } : {
                '& .MuiOutlinedInput-root': {
                  bgcolor: 'transparent'
                }
              }]}
            />
            {(sourceLabel || showFetched) && (
              <Stack direction="row" spacing={1} sx={{ mt: 0.25, ml: 0.5 }}>
                {sourceLabel && (
                  <Typography
                    variant="caption"
                    sx={[{
                      fontSize: '0.7rem'
                    }, isDirty ? {
                      color: 'warning.main'
                    } : {
                      color: 'text.disabled'
                    }]}
                  >
                    Source: {sourceLabel}
                  </Typography>
                )}
                {showFetched && (
                  <Typography
                    variant="caption"
                    sx={{ color: 'text.disabled', fontSize: '0.7rem' }}
                  >
                    Fetched: {fetchedStr.length > 40 ? fetchedStr.slice(0, 37) + '...' : fetchedStr}
                  </Typography>
                )}
              </Stack>
            )}
          </Box>
        </Box>
      </Box>
    );
  };

  if (!audiobook) return null;

  return (
    <Dialog
      open={open}
      onClose={onClose}
      maxWidth="md"
      fullWidth
      PaperProps={{
        sx: { minHeight: '70vh', maxHeight: '90vh' },
      }}
    >
      <DialogTitle sx={{ pb: 1 }}>
        <Stack direction="row" alignItems="center" spacing={1}>
          <Typography variant="h6" component="span">Edit Metadata</Typography>
          {loadingStates && <CircularProgress size={16} />}
        </Stack>
        <Typography variant="caption" color="text.disabled" display="block">
          Edited fields are automatically locked to prevent overwrites from future fetches.
        </Typography>
      </DialogTitle>
      <DialogContent sx={{ pt: 1 }}>
        {error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {error}
          </Alert>
        )}

        <Stack spacing={2} sx={{ mt: 1 }}>
          {/* Title — full width */}
          {renderField('title', 'Title *')}

          {/* Author + Narrator */}
          <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
            {renderField('author', 'Author', { width: '1 1 50%' })}
            {renderField('narrator', 'Narrator', { width: '1 1 50%' })}
          </Stack>

          {/* Series + Series Number */}
          <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
            {renderField('series', 'Series', { width: '1 1 65%' })}
            {renderField('series_number', 'Series Number', { type: 'number', width: '1 1 35%' })}
          </Stack>

          {/* Track / Disk numbers */}
          <Stack direction="row" spacing={2}>
            {renderField('track_number', 'Track Number', { width: '1 1 25%' })}
            {renderField('total_tracks', 'Total Tracks', { type: 'number', width: '1 1 25%' })}
            {renderField('disk_number', 'Disk Number', { width: '1 1 25%' })}
            {renderField('total_disks', 'Total Disks', { type: 'number', width: '1 1 25%' })}
          </Stack>

          {/* Genre + Year */}
          <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
            {renderField('genre', 'Genre', { width: '1 1 50%' })}
            {renderField('year', 'Year', { width: '1 1 50%' })}
          </Stack>

          {/* Language + Publisher */}
          <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
            {renderField('language', 'Language', { width: '1 1 50%' })}
            {renderField('publisher', 'Publisher', { width: '1 1 50%' })}
          </Stack>

          {/* ISBNs */}
          <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
            {renderField('isbn10', 'ISBN-10', { width: '1 1 50%' })}
            {renderField('isbn13', 'ISBN-13', { width: '1 1 50%' })}
          </Stack>

          {/* Description */}
          {renderField('description', 'Description', { multiline: true, rows: 3 })}
        </Stack>
      </DialogContent>
      <DialogActions sx={{ px: 3, py: 2 }}>
        <Button onClick={onClose} disabled={saving}>
          Cancel
        </Button>
        <Button onClick={handleSave} variant="contained" disabled={saving}>
          {saving ? 'Saving...' : 'Save'}
        </Button>
      </DialogActions>
    </Dialog>
  );
};
