// file: web/src/components/bookdetail/BookDetailInfoTab.tsx
// version: 1.1.1
// guid: e5f6a7b8-c9d0-1234-efab-345678901234
// last-edited: 2026-08-07

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Alert,
  Box,
  Chip,
  Grid,
  LinearProgress,
  Paper,
  Rating,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import LabelIcon from '@mui/icons-material/Label';
import type { Book, SegmentTags } from '../../services/api';
import * as api from '../../services/api';
import { formatDuration, formatBytes } from './bookDetailUtils';
import { WhisperIntroPanel } from './WhisperIntroPanel';

export interface BookDetailInfoTabProps {
  book: Book;
  bookId: string;
  singleSelectedId: string | null;
  segmentTags: SegmentTags | null;
  segmentTagsLoading: boolean;
  detailedTags: api.DetailedBookTag[];
  toast: (msg: string, severity: 'success' | 'error' | 'warning' | 'info') => void;
}

export const BookDetailInfoTab = ({
  book,
  bookId,
  singleSelectedId,
  segmentTags,
  segmentTagsLoading,
  detailedTags,
  toast,
}: BookDetailInfoTabProps) => {
  const [ratingOverall, setRatingOverall] = useState<number | null>(null);
  const [ratingStory, setRatingStory] = useState<number | null>(null);
  const [ratingPerformance, setRatingPerformance] = useState<number | null>(null);
  const [ratingNotes, setRatingNotes] = useState<string>('');
  const ratingDebounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Sync rating local state when book is (re)loaded — intentionally on id only — don't overwrite in-progress edits on silent refresh
  useEffect(() => {
    if (!book) return;
    setRatingOverall(book.user_rating_overall ?? null);
    setRatingStory(book.user_rating_story ?? null);
    setRatingPerformance(book.user_rating_performance ?? null);
    setRatingNotes(book.user_rating_notes ?? '');
  }, [book?.id]); // eslint-disable-line react-hooks/exhaustive-deps

  const saveRating = useCallback(
    (patch: api.RatingPatchBody) => {
      if (!bookId) return;
      if (ratingDebounceRef.current) clearTimeout(ratingDebounceRef.current);
      ratingDebounceRef.current = setTimeout(async () => {
        try {
          await api.patchAudiobookRating(bookId, patch);
          toast('Rating saved', 'success');
        } catch {
          toast('Failed to save rating', 'error');
        }
      }, 500);
    },
    [bookId, toast]
  );

  const handleRatingOverallChange = (_: unknown, value: number | null) => {
    setRatingOverall(value);
    saveRating({ overall: value, story: ratingStory, performance: ratingPerformance, notes: ratingNotes || null });
  };

  const handleRatingStoryChange = (_: unknown, value: number | null) => {
    setRatingStory(value);
    saveRating({ overall: ratingOverall, story: value, performance: ratingPerformance, notes: ratingNotes || null });
  };

  const handleRatingPerformanceChange = (_: unknown, value: number | null) => {
    setRatingPerformance(value);
    saveRating({ overall: ratingOverall, story: ratingStory, performance: value, notes: ratingNotes || null });
  };

  const handleRatingNotesBlur = () => {
    saveRating({ overall: ratingOverall, story: ratingStory, performance: ratingPerformance, notes: ratingNotes || null });
  };

  return (
    <>
      <Paper sx={{ p: 3, mb: 3 }}>
        {singleSelectedId && segmentTags ? (
          <>
            {segmentTagsLoading && <LinearProgress sx={{ mb: 2 }} />}
            <Typography variant="subtitle2" color="text.secondary" gutterBottom>
              File-specific info for: {segmentTags.file_path.split('/').pop()}
            </Typography>
            <Grid container spacing={2}>
              {[
                { label: 'Filename', value: segmentTags.file_path.split('/').pop() },
                { label: 'Format', value: segmentTags.format?.toUpperCase() },
                { label: 'Duration', value: formatDuration(segmentTags.duration_sec) },
                {
                  label: 'Size',
                  value: formatBytes(segmentTags.size_bytes),
                },
                {
                  label: 'Track Number',
                  value: segmentTags.track_number != null
                    ? `${segmentTags.track_number}${segmentTags.total_tracks ? ` of ${segmentTags.total_tracks}` : ''}`
                    : undefined,
                },
                { label: 'Codec', value: segmentTags.tags?.codec },
                { label: 'Bitrate', value: segmentTags.tags?.bitrate ? `${segmentTags.tags.bitrate} kbps` : undefined },
                { label: 'Sample Rate', value: segmentTags.tags?.sample_rate ? `${segmentTags.tags.sample_rate} Hz` : undefined },
              ]
                .filter((item) => item.value !== undefined && item.value !== '' && item.value !== null && item.value !== '\u2014')
                .map((item) => (
                  <Grid item xs={12} sm={6} md={4} key={item.label}>
                    <Box
                      sx={{
                        p: 2,
                        borderRadius: 1,
                        bgcolor: 'background.default',
                        border: '1px solid',
                        borderColor: 'divider',
                        height: '100%',
                      }}
                    >
                      <Typography
                        variant="caption"
                        color="text.secondary"
                        sx={{ textTransform: 'uppercase' }}
                      >
                        {item.label}
                      </Typography>
                      <Typography variant="body1">
                        {item.value as string}
                      </Typography>
                    </Box>
                  </Grid>
                ))}
            </Grid>
            {segmentTags.tags_read_error && (
              <Alert severity="warning" sx={{ mt: 2 }}>
                Tag read error: {segmentTags.tags_read_error}
              </Alert>
            )}
            {segmentTags.used_filename_fallback && (
              <Alert severity="info" sx={{ mt: 2 }}>
                Some metadata was extracted from the filename because embedded tags were incomplete.
              </Alert>
            )}
          </>
        ) : (
          <>
            {singleSelectedId && segmentTagsLoading && <LinearProgress sx={{ mb: 2 }} />}
            <Grid container spacing={2}>
              {(() => {
                const authorVal = book.authors && book.authors.length > 0
                  ? book.authors.map((a) => a.name).join(' & ')
                  : book.author_name || '';
                const narratorVal = book.narrators && book.narrators.length > 0
                  ? book.narrators.map((n) => n.name).join(' & ')
                  : book.narrator || '';
                const coreFields = [
                  { label: 'Title', value: book.title || '' },
                  { label: 'Author', value: authorVal },
                  { label: 'Narrator', value: narratorVal },
                  { label: 'Language', value: book.language || '' },
                  { label: 'Series', value: book.series_name ? `${book.series_name}${book.series_position ? ` #${book.series_position}` : ''}` : '' },
                ];
                const dynamicFields = [
                  { label: 'Publisher', value: book.publisher },
                  { label: 'Release Year', value: book.audiobook_release_year ? String(book.audiobook_release_year) : undefined },
                  { label: 'Print Year', value: book.print_year ? String(book.print_year) : undefined },
                  { label: 'ISBN 13', value: book.isbn13 },
                  { label: 'ISBN 10', value: book.isbn10 },
                  { label: 'Genre', value: book.genre },
                  { label: 'Format', value: book.format?.toUpperCase() },
                  { label: 'Codec', value: book.codec },
                  { label: 'Bitrate', value: book.bitrate ? `${book.bitrate} kbps` : undefined },
                  { label: 'Duration', value: book.duration ? formatDuration(book.duration) : undefined },
                  { label: 'Audible Runtime', value: (() => {
                    if (!book.audible_runtime_min) return undefined;
                    const h = Math.floor(book.audible_runtime_min / 60);
                    const m = book.audible_runtime_min % 60;
                    return h > 0 ? `${h}h ${m}m` : `${m}m`;
                  })() },
                  { label: 'Edition', value: book.edition && book.edition !== '0' && book.edition.length <= 50 ? book.edition : undefined },
                  { label: 'Description', value: book.description || (book.edition && book.edition.length > 50 ? book.edition : undefined) },
                  { label: 'Work ID', value: book.work_id },
                ].filter((item) => item.value !== undefined && item.value !== '' && item.value !== null);
                return [...coreFields, ...dynamicFields].map((item) => (
                  <Grid item xs={12} sm={item.label === 'Description' ? 12 : 6} md={item.label === 'Description' ? 12 : 4} key={item.label}>
                    <Box
                      sx={{
                        p: 2,
                        borderRadius: 1,
                        bgcolor: 'background.default',
                        border: '1px solid',
                        borderColor: 'divider',
                        height: '100%',
                      }}
                    >
                      <Typography
                        variant="caption"
                        color="text.secondary"
                        sx={{ textTransform: 'uppercase', letterSpacing: 0.5 }}
                      >
                        {item.label}
                      </Typography>
                      <Typography variant="body1" sx={{ color: item.value ? 'text.primary' : 'text.disabled' }}>
                        {item.value || '\u2014'}
                      </Typography>
                    </Box>
                  </Grid>
                ));
              })()}
              {/* Duration delta warning chip — shown when actual audio differs from Audible runtime by >5 min */}
              {book.duration_delta_sec != null && Math.abs(book.duration_delta_sec) > 300 && (() => {
                const absDelta = Math.abs(book.duration_delta_sec!);
                const sign = book.duration_delta_sec! > 0 ? '+' : '-';
                const totalMin = Math.floor(absDelta / 60);
                const h = Math.floor(totalMin / 60);
                const m = totalMin % 60;
                const deltaLabel = absDelta >= 60
                  ? (h > 0 ? `${sign}${h}h ${m}m off from Audible` : `${sign}${m}m off from Audible`)
                  : `${sign}${absDelta}s off from Audible`;
                return (
                  <Grid item xs={12}>
                    <Tooltip title="Difference between actual audio duration and Audible's listed runtime">
                      <Chip color="warning" label={deltaLabel} size="small" />
                    </Tooltip>
                  </Grid>
                );
              })()}
            </Grid>
          </>
        )}
      </Paper>

      {/* Star rating widget — RATE-2 */}
      <Paper sx={{ p: 3, mb: 3 }}>
        <Typography variant="subtitle1" fontWeight={600} gutterBottom>
          Your Rating
        </Typography>
        <Stack spacing={2}>
          {(
            [
              { label: 'Overall', value: ratingOverall, onChange: handleRatingOverallChange },
              { label: 'Story', value: ratingStory, onChange: handleRatingStoryChange },
              { label: 'Performance', value: ratingPerformance, onChange: handleRatingPerformanceChange },
            ] as const
          ).map(({ label, value, onChange }) => (
            <Box key={label} sx={{ display: 'flex', alignItems: 'center', gap: 2 }}>
              <Typography variant="body2" sx={{ width: 110, flexShrink: 0 }}>
                {label}
              </Typography>
              <Rating
                value={value}
                onChange={onChange}
                precision={0.5}
                max={5}
              />
              {value != null && (
                <Typography variant="caption" color="text.secondary">
                  {value.toFixed(1)} / 5
                </Typography>
              )}
            </Box>
          ))}
          <Box>
            <Typography variant="body2" sx={{ mb: 0.5 }}>
              Notes
            </Typography>
            <TextField
              multiline
              minRows={2}
              maxRows={6}
              fullWidth
              placeholder="Your thoughts on this audiobook…"
              value={ratingNotes}
              onChange={(e) => setRatingNotes(e.target.value)}
              onBlur={handleRatingNotesBlur}
              size="small"
            />
          </Box>
        </Stack>
      </Paper>

      {/* Tags section — Audible Categories + Your Labels (CAT-1 / PR #548) */}
      {detailedTags.length > 0 && (
        <Paper sx={{ p: 3, mb: 3 }}>
          <Typography variant="subtitle1" fontWeight={600} gutterBottom>
            Tags
          </Typography>
          {detailedTags.filter((t) => t.source !== 'user').length > 0 && (
            <>
              <Typography variant="subtitle2" color="text.secondary" gutterBottom>
                Audible Categories
              </Typography>
              <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1, mb: detailedTags.some((t) => t.source === 'user') ? 2 : 0 }}>
                {detailedTags.filter((t) => t.source !== 'user').map((t) => (
                  <Chip key={t.tag} label={t.tag} size="small" variant="outlined" icon={<LabelIcon />} />
                ))}
              </Box>
            </>
          )}
          {detailedTags.filter((t) => t.source === 'user').length > 0 && (
            <>
              <Typography variant="subtitle2" color="text.secondary" gutterBottom>
                Your Labels
              </Typography>
              <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1 }}>
                {detailedTags.filter((t) => t.source === 'user').map((t) => (
                  <Chip key={t.tag} label={t.tag} size="small" />
                ))}
              </Box>
            </>
          )}
        </Paper>
      )}

      <WhisperIntroPanel book={book} />
    </>
  );
};
