// file: web/src/components/audiobooks/BulkRatingDialog.tsx
// version: 1.0.2
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890

import React, { useState } from 'react';
import {
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Button,
  TextField,
  Typography,
  Stack,
  Alert,
  CircularProgress,
  LinearProgress,
  Box,
} from '@mui/material';
import Rating from '@mui/material/Rating';
import StarIcon from '@mui/icons-material/Star';
import * as api from '../../services/api';

interface BulkRatingDialogProps {
  open: boolean;
  onClose: () => void;
  bookIds: string[];
  onComplete?: () => void;
}

export const BulkRatingDialog: React.FC<BulkRatingDialogProps> = ({
  open,
  onClose,
  bookIds,
  onComplete,
}) => {
  const [overall, setOverall] = useState<number | null>(null);
  const [story, setStory] = useState<number | null>(null);
  const [performance, setPerformance] = useState<number | null>(null);
  const [notes, setNotes] = useState('');
  const [loading, setLoading] = useState(false);
  const [progress, setProgress] = useState<{ completed: number; total: number } | null>(null);
  const [result, setResult] = useState<{ success: number; failed: number } | null>(null);
  const [error, setError] = useState<string | null>(null);

  const hasAnyRating =
    overall !== null || story !== null || performance !== null || notes.trim() !== '';

  const handleApply = async () => {
    if (!hasAnyRating) return;
    setLoading(true);
    setError(null);
    setResult(null);
    setProgress({ completed: 0, total: bookIds.length });

    const body: api.RatingPatchBody = {};
    if (overall !== null) body.overall = overall;
    if (story !== null) body.story = story;
    if (performance !== null) body.performance = performance;
    if (notes.trim() !== '') body.notes = notes.trim();

    let success = 0;
    let failed = 0;

    for (let i = 0; i < bookIds.length; i++) {
      try {
        await api.patchAudiobookRating(bookIds[i], body);
        success++;
      } catch (_err) {
        failed++;
      }
      setProgress({ completed: i + 1, total: bookIds.length });
    }

    setResult({ success, failed });
    setLoading(false);
    if (failed === 0) {
      onComplete?.();
    }
  };

  const handleClose = () => {
    if (loading) return;
    setOverall(null);
    setStory(null);
    setPerformance(null);
    setNotes('');
    setProgress(null);
    setResult(null);
    setError(null);
    onClose();
  };

  return (
    <Dialog open={open} onClose={handleClose} maxWidth="sm" fullWidth>
      <DialogTitle>
        Rate {bookIds.length} book{bookIds.length !== 1 ? 's' : ''}
      </DialogTitle>
      <DialogContent>
        <Stack spacing={3} sx={{ mt: 1 }}>
          <Typography
            variant="body2"
            sx={{
              color: 'text.secondary',
            }}
          >
            Set ratings for all selected books. Leave a dimension blank to keep each book's existing
            value.
          </Typography>

          <Box>
            <Typography variant="subtitle2" gutterBottom>
              Overall
            </Typography>
            <Rating
              value={overall}
              onChange={(_e, v) => setOverall(v)}
              precision={0.5}
              emptyIcon={<StarIcon style={{ opacity: 0.55 }} fontSize="inherit" />}
            />
          </Box>

          <Box>
            <Typography variant="subtitle2" gutterBottom>
              Story
            </Typography>
            <Rating
              value={story}
              onChange={(_e, v) => setStory(v)}
              precision={0.5}
              emptyIcon={<StarIcon style={{ opacity: 0.55 }} fontSize="inherit" />}
            />
          </Box>

          <Box>
            <Typography variant="subtitle2" gutterBottom>
              Performance
            </Typography>
            <Rating
              value={performance}
              onChange={(_e, v) => setPerformance(v)}
              precision={0.5}
              emptyIcon={<StarIcon style={{ opacity: 0.55 }} fontSize="inherit" />}
            />
          </Box>

          <TextField
            label="Notes"
            multiline
            minRows={2}
            maxRows={5}
            size="small"
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
            placeholder="Optional notes applied to all selected books..."
            disabled={loading}
          />

          {loading && progress && (
            <Box>
              <Typography
                variant="caption"
                sx={{
                  color: 'text.secondary',
                }}
              >
                Rating {progress.completed} / {progress.total}...
              </Typography>
              <LinearProgress
                variant="determinate"
                value={(progress.completed / progress.total) * 100}
                sx={{ mt: 0.5 }}
              />
            </Box>
          )}

          {error && <Alert severity="error">{error}</Alert>}

          {result && (
            <Alert severity={result.failed === 0 ? 'success' : 'warning'}>
              {result.failed === 0
                ? `Ratings applied to ${result.success} book${result.success !== 1 ? 's' : ''}.`
                : `Applied to ${result.success} books; ${result.failed} failed.`}
            </Alert>
          )}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={handleClose} disabled={loading}>
          {result ? 'Close' : 'Cancel'}
        </Button>
        {!result && (
          <Button
            variant="contained"
            onClick={handleApply}
            disabled={loading || !hasAnyRating}
            startIcon={loading ? <CircularProgress size={16} /> : undefined}
          >
            {loading ? 'Applying...' : `Apply to all ${bookIds.length}`}
          </Button>
        )}
      </DialogActions>
    </Dialog>
  );
};
