// file: web/src/components/bookdetail/BookDetailHeader.tsx
// version: 1.1.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-08-23

import { useEffect, useState } from 'react';
import {
  Avatar,
  Box,
  Button,
  Chip,
  Dialog,
  DialogContent,
  Stack,
  Tooltip,
  Typography,
} from '@mui/material';
import ArrowBackIcon from '@mui/icons-material/ArrowBack';
import AccessTimeIcon from '@mui/icons-material/AccessTime';
import InfoIcon from '@mui/icons-material/Info';
import CompareIcon from '@mui/icons-material/Compare';
import type { Book, BookFile, BookSegment } from '../../services/api';
import ReadStatusChip from '../audiobooks/ReadStatusChip';
import { formatDateTime } from './bookDetailUtils';

export interface BookDetailHeaderProps {
  book: Book;
  bookFiles: BookFile[];
  segments: BookSegment[];
  itunesLinked: boolean;
  itunesPidCount: number;
  /**
   * Every book in this book's version group, as passed to
   * BookDetailVersionGroup below. Used only for the count on the "Version
   * Group" chip -- deliberately the SAME array the tray renders, so the two
   * can never disagree. See versionCount for the empty-array case.
   */
  versions: Book[];
  activeTab: 'info' | 'files';
  onBack: () => void;
  onSetActiveTab: (tab: 'info' | 'files') => void;
}

export const BookDetailHeader = ({
  book,
  bookFiles,
  segments,
  itunesLinked,
  itunesPidCount,
  versions,
  onBack,
  onSetActiveTab,
}: BookDetailHeaderProps) => {
  // Match BookDetailVersionGroup's own fallback exactly: it renders
  // `versions.length > 0 ? versions : [book]`, so an empty array still shows
  // one row (this book). Counting versions.length here would print "0" above
  // a tray listing one entry. A header count that disagrees with the list
  // underneath it is worse than no count at all, so this mirrors the fallback
  // rather than counting the raw prop.
  const versionCount = versions.length > 0 ? versions.length : 1;

  const [coverError, setCoverError] = useState(false);
  const [coverLightboxOpen, setCoverLightboxOpen] = useState(false);

  useEffect(() => {
    setCoverError(false);
  }, [book.cover_url]);

  const coverImageUrl = book.cover_url
    ? book.cover_url.startsWith('/')
      ? book.cover_url
      : `/api/v1/covers/proxy?url=${encodeURIComponent(book.cover_url)}`
    : `/api/v1/audiobooks/${book.id}/cover`;

  const coverLetter = (book.title || 'A')[0]?.toUpperCase();
  const isSoftDeleted = book.marked_for_deletion;
  const isQuarantined = !!book.quarantined_at;

  return (
    <>
      <Stack
        direction="row"
        spacing={2}
        sx={{
          alignItems: 'center',
          mb: 3,
          flexWrap: 'wrap',
        }}
      >
        <Button startIcon={<ArrowBackIcon />} variant="text" onClick={onBack}>
          Back to Library
        </Button>
        <Stack
          direction="row"
          spacing={2}
          sx={{
            alignItems: 'center',
          }}
        >
          {coverError ? (
            <Avatar
              sx={{
                bgcolor: 'primary.main',
                width: 120,
                height: 120,
                fontSize: 48,
                borderRadius: 2,
              }}
              variant="rounded"
            >
              {coverLetter}
            </Avatar>
          ) : (
            <Box
              component="img"
              src={coverImageUrl}
              alt={`Cover art for ${book.title || 'Untitled'}`}
              onError={() => setCoverError(true)}
              onClick={() => setCoverLightboxOpen(true)}
              sx={{
                width: 120,
                height: 120,
                objectFit: 'cover',
                borderRadius: 2,
                boxShadow: 2,
                cursor: 'pointer',
                '&:hover': { opacity: 0.85 },
              }}
            />
          )}
          <Stack spacing={0.5}>
            <Box
              sx={{
                display: 'flex',
                alignItems: 'center',
                gap: 1,
                flexWrap: 'wrap',
              }}
            >
              <Typography variant="h4" component="h1">
                {book.title || 'Untitled'}
              </Typography>
              {isSoftDeleted && <Chip label="Soft Deleted" color="warning" size="small" />}
              {isQuarantined && (
                <Tooltip title={book.quarantine_reason || 'Quarantined'}>
                  <Chip label="Failed" color="error" size="small" />
                </Tooltip>
              )}
              {book.library_state && (
                <Chip
                  label={`State: ${book.library_state}`}
                  color={
                    book.library_state === 'imported'
                      ? 'warning'
                      : book.library_state === 'organized'
                        ? 'success'
                        : 'default'
                  }
                  size="small"
                />
              )}
              {book.is_primary_version && <Chip label="Primary Version" color="primary" />}
              {bookFiles.some((f) => f.imported_from_deluge_at) && (
                <Tooltip title="At least one file was imported via Deluge">
                  <Chip
                    label="Imported from Deluge"
                    color="secondary"
                    size="small"
                    variant="outlined"
                  />
                </Tooltip>
              )}
              <ReadStatusChip bookId={book.id} />
            </Box>
            <Typography
              variant="subtitle1"
              sx={{
                color: 'text.secondary',
              }}
            >
              {book.authors && book.authors.length > 0
                ? `By ${book.authors.map((a) => a.name).join(' & ')}`
                : book.author_name
                  ? `By ${book.author_name}`
                  : 'Unknown Author'}
            </Typography>
            <Stack
              direction="row"
              spacing={1}
              sx={{
                flexWrap: 'wrap',
                mt: 0.5,
              }}
            >
              <Chip
                icon={<AccessTimeIcon />}
                label={`Created ${formatDateTime(book.created_at)}`}
                variant="outlined"
                size="small"
              />
              <Chip
                icon={<InfoIcon />}
                label={`Updated ${formatDateTime(book.updated_at)}`}
                variant="outlined"
                size="small"
              />
              {book.version_group_id &&
                (() => {
                  const anyMissing =
                    book.file_exists === false || segments.some((s) => s.file_exists === false);
                  return (
                    <Chip
                      icon={<CompareIcon />}
                      label={`Version Group (${versionCount})`}
                      color={anyMissing ? 'error' : 'success'}
                      variant="outlined"
                      size="small"
                    />
                  );
                })()}
              {itunesLinked && (
                <Chip
                  label={`iTunes Linked (${itunesPidCount} PID${itunesPidCount !== 1 ? 's' : ''})`}
                  color="info"
                  variant="outlined"
                  size="small"
                  clickable
                  onClick={() => onSetActiveTab('files')}
                />
              )}
            </Stack>
          </Stack>
        </Stack>
      </Stack>

      {/* Cover image lightbox */}
      <Dialog open={coverLightboxOpen} onClose={() => setCoverLightboxOpen(false)} maxWidth="sm">
        <DialogContent sx={{ p: 1 }}>
          <Box
            component="img"
            src={coverImageUrl}
            alt={`Cover art for ${book.title || 'Untitled'}`}
            sx={{ width: '100%', maxWidth: 600, display: 'block' }}
          />
        </DialogContent>
      </Dialog>
    </>
  );
};
