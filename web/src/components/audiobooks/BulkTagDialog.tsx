// file: web/src/components/audiobooks/BulkTagDialog.tsx
// version: 1.0.1
// guid: c4d5e6f7-a8b9-4c0d-1e2f-3a4b5c6d7e8f

import React, { useState } from 'react';
import {
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Button,
  Autocomplete,
  TextField,
  Chip,
  Typography,
  Stack,
  Alert,
  CircularProgress,
} from '@mui/material';
import * as api from '../../services/api';

interface BulkTagDialogProps {
  open: boolean;
  onClose: () => void;
  bookIds: string[];
  allTags?: string[];
  onComplete?: () => void;
}

export const BulkTagDialog: React.FC<BulkTagDialogProps> = ({
  open,
  onClose,
  bookIds,
  allTags = [],
  onComplete,
}) => {
  const [addTags, setAddTags] = useState<string[]>([]);
  const [removeTags, setRemoveTags] = useState<string[]>([]);
  const [addInput, setAddInput] = useState('');
  const [removeInput, setRemoveInput] = useState('');
  const [loading, setLoading] = useState(false);
  const [result, setResult] = useState<{ affected: number } | null>(null);
  const [error, setError] = useState<string | null>(null);

  const handleApply = async () => {
    if (addTags.length === 0 && removeTags.length === 0) return;
    setLoading(true);
    setError(null);
    setResult(null);
    try {
      const affected = await api.bulkUpdateTags(bookIds, addTags, removeTags);
      setResult({ affected });
      onComplete?.();
    } catch (_err) {
      setError(_err instanceof Error ? _err.message : 'Failed to update tags');
    } finally {
      setLoading(false);
    }
  };

  const handleClose = () => {
    setAddTags([]);
    setRemoveTags([]);
    setAddInput('');
    setRemoveInput('');
    setResult(null);
    setError(null);
    onClose();
  };

  const handleAddTagInput = (value: string) => {
    const normalized = value.trim().toLowerCase();
    if (normalized && !addTags.includes(normalized)) {
      setAddTags([...addTags, normalized]);
    }
    setAddInput('');
  };

  const handleRemoveTagInput = (value: string) => {
    const normalized = value.trim().toLowerCase();
    if (normalized && !removeTags.includes(normalized)) {
      setRemoveTags([...removeTags, normalized]);
    }
    setRemoveInput('');
  };

  return (
    <Dialog open={open} onClose={handleClose} maxWidth="sm" fullWidth>
      <DialogTitle>Manage Tags for {bookIds.length} books</DialogTitle>
      <DialogContent>
        <Stack spacing={3} sx={{ mt: 1 }}>
          <div>
            <Typography variant="subtitle2" gutterBottom>
              Add Tags
            </Typography>
            <Autocomplete
              multiple
              freeSolo
              size="small"
              options={allTags.filter((t) => !addTags.includes(t))}
              value={addTags}
              onChange={(_e, value) => setAddTags(value.map((v) => v.trim().toLowerCase()))}
              inputValue={addInput}
              onInputChange={(_e, value) => setAddInput(value)}
              renderTags={(value, getTagProps) =>
                value.map((tag, index) => {
                  const { key, ...rest } = getTagProps({ index });
                  return (
                    <Chip
                      key={key}
                      label={tag}
                      size="small"
                      variant="outlined"
                      color="primary"
                      {...rest}
                    />
                  );
                })
              }
              renderInput={(params) => (
                <TextField
                  {...params}
                  placeholder="Type tag and press Enter..."
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && addInput.trim()) {
                      e.preventDefault();
                      handleAddTagInput(addInput);
                    }
                  }}
                />
              )}
            />
          </div>

          <div>
            <Typography variant="subtitle2" gutterBottom>
              Remove Tags
            </Typography>
            <Autocomplete
              multiple
              freeSolo
              size="small"
              options={allTags.filter((t) => !removeTags.includes(t))}
              value={removeTags}
              onChange={(_e, value) => setRemoveTags(value.map((v) => v.trim().toLowerCase()))}
              inputValue={removeInput}
              onInputChange={(_e, value) => setRemoveInput(value)}
              renderTags={(value, getTagProps) =>
                value.map((tag, index) => {
                  const { key, ...rest } = getTagProps({ index });
                  return (
                    <Chip
                      key={key}
                      label={tag}
                      size="small"
                      variant="outlined"
                      color="secondary"
                      {...rest}
                    />
                  );
                })
              }
              renderInput={(params) => (
                <TextField
                  {...params}
                  placeholder="Type tag and press Enter..."
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && removeInput.trim()) {
                      e.preventDefault();
                      handleRemoveTagInput(removeInput);
                    }
                  }}
                />
              )}
            />
          </div>

          {error && <Alert severity="error">{error}</Alert>}
          {result && <Alert severity="success">Tags updated for {result.affected} books.</Alert>}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={handleClose}>{result ? 'Close' : 'Cancel'}</Button>
        {!result && (
          <Button
            variant="contained"
            onClick={handleApply}
            disabled={loading || (addTags.length === 0 && removeTags.length === 0)}
            startIcon={loading ? <CircularProgress size={16} /> : undefined}
          >
            {loading ? 'Applying...' : 'Apply'}
          </Button>
        )}
      </DialogActions>
    </Dialog>
  );
};
