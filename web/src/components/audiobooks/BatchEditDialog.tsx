// file: web/src/components/audiobooks/BatchEditDialog.tsx
// version: 1.1.1
// guid: 5b6c7d8e-9f0a-1b2c-3d4e-5f6a7b8c9d0e

import React, { useState } from 'react';
import {
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Button,
  TextField,
  Grid,
  Box,
  Alert,
  FormControlLabel,
  Checkbox,
  Tooltip,
  Typography,
} from '@mui/material';
import type { Audiobook } from '../../types';

interface BatchEditDialogProps {
  open: boolean;
  audiobooks: Audiobook[];
  onClose: () => void;
  // onSave is called ONCE with the common updates (same values
  // for all books). When series_position auto-increment is on,
  // the dialog calls onSavePerBook instead — one call per book
  // with a different series_position each time.
  onSave: (updates: Partial<Audiobook>) => Promise<void>;
  // onSavePerBook is optional — when absent and auto-increment
  // is needed, the dialog falls back to calling onSave multiple
  // times (less efficient but always works).
  onSavePerBook?: (bookId: string, updates: Partial<Audiobook>) => Promise<void>;
}

interface FieldUpdate {
  enabled: boolean;
  value: string | number;
}

export const BatchEditDialog: React.FC<BatchEditDialogProps> = ({
  open,
  audiobooks,
  onClose,
  onSave,
  onSavePerBook,
}) => {
  const [updates, setUpdates] = useState<Record<string, FieldUpdate>>({
    author: { enabled: false, value: '' },
    narrator: { enabled: false, value: '' },
    series: { enabled: false, value: '' },
    series_position: { enabled: false, value: 1 },
    genre: { enabled: false, value: '' },
    language: { enabled: false, value: '' },
    publisher: { enabled: false, value: '' },
    year: { enabled: false, value: '' },
  });
  // Auto-increment: when series_position is enabled AND this is
  // checked, each book gets an incrementing series_position
  // starting from the value in the field. Typical use: select 10
  // books in a series, set starting position to 1, check
  // auto-increment, and all 10 get positions 1–10.
  const [autoIncrement, setAutoIncrement] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleToggle = (field: string) => {
    setUpdates((prev) => ({
      ...prev,
      [field]: { ...prev[field], enabled: !prev[field].enabled },
    }));
  };

  const handleChange = (field: string, value: string | number) => {
    setUpdates((prev) => ({
      ...prev,
      [field]: { ...prev[field], value },
    }));
  };

  const handleSave = async () => {
    setSaving(true);
    setError(null);

    try {
      // Build the common (non-auto-incrementing) field set.
      const changedFields: Partial<Audiobook> = {};
      Object.entries(updates).forEach(([field, update]) => {
        if (update.enabled) {
          // Skip series_position if auto-increment is on — it
          // gets handled per-book below.
          if (field === 'series_position' && autoIncrement) return;
          changedFields[field as keyof Audiobook] = update.value as never;
        }
      });

      // Auto-increment path: each book gets a different
      // series_position. Uses onSavePerBook when available
      // (single PUT per book) or falls back to onSave in
      // a loop (batch endpoint can't do per-book values).
      if (updates.series_position.enabled && autoIncrement) {
        const start = Number(updates.series_position.value) || 1;
        const saveFn = onSavePerBook || (async (_id: string, u: Partial<Audiobook>) => onSave(u));
        let i = 0;
        for (const ab of audiobooks) {
          const perBook = {
            ...changedFields,
            series_position: start + i,
          } as Partial<Audiobook>;
          await saveFn(ab.id, perBook);
          i++;
        }
      } else {
        await onSave(changedFields);
      }
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update audiobooks');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onClose={onClose} maxWidth="md" fullWidth>
      <DialogTitle>
        Batch Edit Metadata ({audiobooks.length} audiobook
        {audiobooks.length !== 1 ? 's' : ''})
      </DialogTitle>
      <DialogContent>
        <Alert severity="info" sx={{ mb: 2 }}>
          Select the fields you want to update. Only checked fields will be modified for all
          selected audiobooks.
        </Alert>

        {error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {error}
          </Alert>
        )}

        <Box sx={{ mt: 2 }}>
          <Grid container spacing={2}>
            <Grid size={12}>
              <FormControlLabel
                control={
                  <Checkbox
                    checked={updates.author.enabled}
                    onChange={() => handleToggle('author')}
                  />
                }
                label="Author"
              />
              <TextField
                fullWidth
                placeholder="Author name"
                value={updates.author.value}
                onChange={(e) => handleChange('author', e.target.value)}
                disabled={!updates.author.enabled}
                sx={{ mt: 1 }}
              />
            </Grid>

            <Grid size={12}>
              <FormControlLabel
                control={
                  <Checkbox
                    checked={updates.narrator.enabled}
                    onChange={() => handleToggle('narrator')}
                  />
                }
                label="Narrator"
              />
              <TextField
                fullWidth
                placeholder="Narrator name"
                value={updates.narrator.value}
                onChange={(e) => handleChange('narrator', e.target.value)}
                disabled={!updates.narrator.enabled}
                sx={{ mt: 1 }}
              />
            </Grid>

            <Grid size={12}>
              <FormControlLabel
                control={
                  <Checkbox
                    checked={updates.series.enabled}
                    onChange={() => handleToggle('series')}
                  />
                }
                label="Series"
              />
              <TextField
                fullWidth
                placeholder="Series name"
                value={updates.series.value}
                onChange={(e) => handleChange('series', e.target.value)}
                disabled={!updates.series.enabled}
                sx={{ mt: 1 }}
              />
            </Grid>

            <Grid
              size={{
                xs: 12,
                sm: 6,
              }}
            >
              <FormControlLabel
                control={
                  <Checkbox
                    checked={updates.series_position.enabled}
                    onChange={() => handleToggle('series_position')}
                  />
                }
                label="Series Position"
              />
              <TextField
                fullWidth
                type="number"
                placeholder="Starting position"
                value={updates.series_position.value}
                onChange={(e) => handleChange('series_position', parseInt(e.target.value) || 1)}
                disabled={!updates.series_position.enabled}
                sx={{ mt: 1 }}
              />
              {updates.series_position.enabled && (
                <Tooltip title="Each book gets an incrementing position starting from the value above. Books are numbered in the order they appear in the selection.">
                  <FormControlLabel
                    control={
                      <Checkbox
                        checked={autoIncrement}
                        onChange={(e) => setAutoIncrement(e.target.checked)}
                        size="small"
                      />
                    }
                    label={<Typography variant="body2">Auto-increment (1, 2, 3, …)</Typography>}
                    sx={{ mt: 0.5 }}
                  />
                </Tooltip>
              )}
            </Grid>

            <Grid
              size={{
                xs: 12,
                sm: 6,
              }}
            >
              <FormControlLabel
                control={
                  <Checkbox
                    checked={updates.genre.enabled}
                    onChange={() => handleToggle('genre')}
                  />
                }
                label="Genre"
              />
              <TextField
                fullWidth
                placeholder="Genre"
                value={updates.genre.value}
                onChange={(e) => handleChange('genre', e.target.value)}
                disabled={!updates.genre.enabled}
                sx={{ mt: 1 }}
              />
            </Grid>

            <Grid
              size={{
                xs: 12,
                sm: 6,
              }}
            >
              <FormControlLabel
                control={
                  <Checkbox
                    checked={updates.language.enabled}
                    onChange={() => handleToggle('language')}
                  />
                }
                label="Language"
              />
              <TextField
                fullWidth
                placeholder="Language"
                value={updates.language.value}
                onChange={(e) => handleChange('language', e.target.value)}
                disabled={!updates.language.enabled}
                sx={{ mt: 1 }}
              />
            </Grid>

            <Grid
              size={{
                xs: 12,
                sm: 6,
              }}
            >
              <FormControlLabel
                control={
                  <Checkbox
                    checked={updates.publisher.enabled}
                    onChange={() => handleToggle('publisher')}
                  />
                }
                label="Publisher"
              />
              <TextField
                fullWidth
                placeholder="Publisher"
                value={updates.publisher.value}
                onChange={(e) => handleChange('publisher', e.target.value)}
                disabled={!updates.publisher.enabled}
                sx={{ mt: 1 }}
              />
            </Grid>

            <Grid
              size={{
                xs: 12,
                sm: 6,
              }}
            >
              <FormControlLabel
                control={
                  <Checkbox checked={updates.year.enabled} onChange={() => handleToggle('year')} />
                }
                label="Year"
              />
              <TextField
                fullWidth
                type="number"
                placeholder="Year"
                value={updates.year.value}
                onChange={(e) => handleChange('year', parseInt(e.target.value) || 0)}
                disabled={!updates.year.enabled}
                sx={{ mt: 1 }}
              />
            </Grid>
          </Grid>
        </Box>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={saving}>
          Cancel
        </Button>
        <Button onClick={handleSave} variant="contained" disabled={saving}>
          {saving
            ? 'Updating...'
            : `Update ${audiobooks.length} audiobook${audiobooks.length !== 1 ? 's' : ''}`}
        </Button>
      </DialogActions>
    </Dialog>
  );
};
