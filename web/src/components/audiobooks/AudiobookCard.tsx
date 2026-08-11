// file: web/src/components/audiobooks/AudiobookCard.tsx
// version: 1.14.0
// guid: 8a9b0c1d-2e3f-4a5b-6c7d-8e9f0a1b2c3d
// last-edited: 2026-08-10
import React from 'react';
import {
  Card,
  CardContent,
  CardMedia,
  Typography,
  Chip,
  Box,
  IconButton,
  Menu,
  MenuItem,
  Checkbox,
  Tooltip,
} from '@mui/material';
import {
  MoreVert as MoreVertIcon,
  Edit as EditIcon,
  Delete as DeleteIcon,
  Compare as CompareIcon,
  CloudDownload as CloudDownloadIcon,
  Psychology as PsychologyIcon,
  LocalOffer as LocalOfferIcon,
} from '@mui/icons-material';
import type { Audiobook } from '../../types';
import type { ColumnDefinition } from '../../config/columnDefinitions';
import { formatTagLabel, shouldRenderTagChip } from '../../utils/tagDisplay';

interface AudiobookCardProps {
  audiobook: Audiobook;
  onEdit?: (audiobook: Audiobook) => void;
  onDelete?: (audiobook: Audiobook) => void;
  onClick?: (audiobook: Audiobook) => void;
  onVersionManage?: (audiobook: Audiobook) => void;
  onFetchMetadata?: (audiobook: Audiobook) => void;
  onParseWithAI?: (audiobook: Audiobook) => void;
  selectable?: boolean;
  selected?: boolean;
  onToggleSelect?: (audiobook: Audiobook, event?: React.MouseEvent) => void;
  columns?: ColumnDefinition[];
  visibleColumnIds?: string[];
}

export const AudiobookCard: React.FC<AudiobookCardProps> = ({
  audiobook,
  onEdit,
  onDelete,
  onClick,
  onVersionManage,
  onFetchMetadata,
  onParseWithAI,
  selectable = false,
  selected = false,
  onToggleSelect,
  columns,
  visibleColumnIds,
}) => {
  const [anchorEl, setAnchorEl] = React.useState<null | HTMLElement>(null);
  const open = Boolean(anchorEl);

  const handleMenuClick = (event: React.MouseEvent<HTMLElement>) => {
    event.stopPropagation();
    setAnchorEl(event.currentTarget);
  };

  const handleClose = () => {
    setAnchorEl(null);
  };

  const handleEdit = (event: React.MouseEvent) => {
    event.stopPropagation();
    handleClose();
    onEdit?.(audiobook);
  };

  const handleDelete = (event: React.MouseEvent) => {
    event.stopPropagation();
    handleClose();
    onDelete?.(audiobook);
  };

  const handleVersionManage = (event: React.MouseEvent) => {
    event.stopPropagation();
    handleClose();
    onVersionManage?.(audiobook);
  };

  const handleFetchMetadata = (event: React.MouseEvent) => {
    event.stopPropagation();
    handleClose();
    onFetchMetadata?.(audiobook);
  };

  const handleParseWithAI = (event: React.MouseEvent) => {
    event.stopPropagation();
    handleClose();
    onParseWithAI?.(audiobook);
  };

  const handleCardClick = () => {
    onClick?.(audiobook);
  };

  const handleSelectToggle = (event: React.MouseEvent) => {
    event.stopPropagation();
    onToggleSelect?.(audiobook, event);
  };

  // Placeholder cover art with first letter of title
  const getCoverPlaceholder = () => {
    return audiobook.title?.charAt(0).toUpperCase() || '?';
  };

  // metadata:source:* tags never render as chips here — they stay
  // filter-only (see FilterSidebar). Filter BEFORE slicing to 3 so the
  // "+N more" overflow count below reflects the actually-visible set.
  const visibleTags = (audiobook.tags ?? []).filter(shouldRenderTagChip);

  return (
    <Card
      sx={[{
        height: '100%',
        display: 'flex',
        flexDirection: 'column',
        transition: 'transform 0.2s, box-shadow 0.2s'
      }, onClick ? {
        cursor: 'pointer'
      } : {
        cursor: 'default'
      }, onClick ? {
        '&:hover': {
          transform: 'translateY(-4px)'
        }
      } : {
        '&:hover': {
          transform: 'none'
        }
      }, onClick ? {
        '&:hover': {
          boxShadow: 6
        }
      } : {
        '&:hover': {
          boxShadow: 1
        }
      }]}
      onClick={handleCardClick}
    >
      <Box sx={{ position: 'relative' }}>
        {selectable && (
          <Checkbox
            checked={selected}
            onClick={handleSelectToggle}
            inputProps={{
              'aria-label': `Select ${audiobook.title || 'audiobook'}`,
            }}
            sx={{
              position: 'absolute',
              top: 8,
              left: 8,
              bgcolor: 'background.paper',
              borderRadius: 1,
              zIndex: 1,
            }}
          />
        )}
        {audiobook.cover_url ? (
          <CardMedia
            component="img"
            height="240"
            image={audiobook.cover_url.startsWith('/api/') ? audiobook.cover_url : `/api/v1/covers/proxy?url=${encodeURIComponent(audiobook.cover_url)}`}
            alt={audiobook.title || 'Audiobook cover'}
            loading="lazy"
            sx={{ objectFit: 'contain', bgcolor: 'grey.900' }}
          />
        ) : (
          <Box
            sx={{
              height: 240,
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              bgcolor: 'primary.main',
              color: 'primary.contrastText',
            }}
          >
            <Typography variant="h1" component="div">
              {getCoverPlaceholder()}
            </Typography>
          </Box>
        )}
        <IconButton
          sx={{
            position: 'absolute',
            top: 8,
            right: 8,
            bgcolor: 'background.paper',
            '&:hover': { bgcolor: 'background.default' },
          }}
          onClick={handleMenuClick}
          size="small"
        >
          <MoreVertIcon />
        </IconButton>
      </Box>

      <CardContent
        sx={{ flexGrow: 1, display: 'flex', flexDirection: 'column' }}
      >
        <Typography
          gutterBottom
          variant="h6"
          component="h2"
          noWrap
          title={audiobook.title || 'Untitled'}
        >
          {audiobook.title || 'Untitled'}
        </Typography>

        <Typography
          variant="body2"
          color="text.secondary"
          noWrap
          title={audiobook.author || 'Unknown Author'}
        >
          {audiobook.author || 'Unknown Author'}
        </Typography>

        {audiobook.series && (
          <Typography
            variant="caption"
            color="text.secondary"
            noWrap
            sx={{ mt: 0.5 }}
          >
            {audiobook.series}
            {audiobook.series_number && ` #${audiobook.series_number}`}
          </Typography>
        )}

        {audiobook.narrator && (
          <Typography
            variant="caption"
            color="text.secondary"
            noWrap
            sx={{ mt: 0.5 }}
          >
            Narrated by: {audiobook.narrator}
          </Typography>
        )}

        {/* Extra fields from columns configuration */}
        {columns && visibleColumnIds && (
          <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.25, mt: 1 }}>
            {columns
              .filter(
                (col) =>
                  visibleColumnIds.includes(col.id) &&
                  !['title', 'author', 'narrator', 'series', 'series_number'].includes(col.id)
              )
              .map((col) => {
                const raw = col.accessor(audiobook);
                if (raw == null) return null;
                const value = col.formatter ? col.formatter(raw) : String(raw);
                if (!value) return null;
                return (
                  <Typography
                    key={col.id}
                    variant="caption"
                    color="text.secondary"
                    noWrap
                    title={`${col.label}: ${value}`}
                  >
                    <strong>{col.label}:</strong> {value}
                  </Typography>
                );
              })}
          </Box>
        )}

        <Box
          sx={{
            mt: 'auto',
            pt: 1,
            display: 'flex',
            flexWrap: 'wrap',
            gap: 0.5,
          }}
        >
          {audiobook.quarantined_at && (
            <Tooltip title={audiobook.quarantine_reason || 'Quarantined'}>
              <Chip label="Failed" size="small" color="error" />
            </Tooltip>
          )}
          {audiobook.metadata_updated_at &&
            (!audiobook.last_written_at ||
              new Date(audiobook.last_written_at) < new Date(audiobook.metadata_updated_at)) && (
            <Tooltip title="Metadata saved to DB but not yet written to file tags">
              <Chip label="Write pending" size="small" color="warning" variant="outlined" />
            </Tooltip>
          )}
          {audiobook.version_group_id && (
            <Chip
              label="Multiple Versions"
              size="small"
              color="info"
              icon={<CompareIcon />}
            />
          )}
          {audiobook.genre && (
            <Chip label={audiobook.genre} size="small" variant="outlined" />
          )}
          {audiobook.language && (
            <Chip label={audiobook.language} size="small" variant="outlined" />
          )}
          {visibleTags.slice(0, 3).map((tag) => (
            <Chip
              key={tag}
              label={formatTagLabel(tag)}
              size="small"
              variant="outlined"
              color="secondary"
              icon={<LocalOfferIcon />}
            />
          ))}
          {visibleTags.length > 3 && (
            <Chip
              label={`+${visibleTags.length - 3}`}
              size="small"
              variant="outlined"
              color="secondary"
            />
          )}
        </Box>
      </CardContent>

      <Menu anchorEl={anchorEl} open={open} onClose={handleClose}>
        {onEdit && (
          <MenuItem onClick={handleEdit}>
            <EditIcon sx={{ mr: 1 }} fontSize="small" />
            Edit
          </MenuItem>
        )}
        {onVersionManage && (
          <MenuItem onClick={handleVersionManage}>
            <CompareIcon sx={{ mr: 1 }} fontSize="small" />
            Manage Versions
          </MenuItem>
        )}
        {onFetchMetadata && (
          <MenuItem onClick={handleFetchMetadata}>
            <CloudDownloadIcon sx={{ mr: 1 }} fontSize="small" />
            Fetch Metadata
          </MenuItem>
        )}
        {onParseWithAI && (
          <MenuItem onClick={handleParseWithAI}>
            <PsychologyIcon sx={{ mr: 1 }} fontSize="small" />
            Parse with AI
          </MenuItem>
        )}
        {onDelete && (
          <MenuItem onClick={handleDelete}>
            <DeleteIcon sx={{ mr: 1 }} fontSize="small" />
            Delete
          </MenuItem>
        )}
      </Menu>
    </Card>
  );
};
