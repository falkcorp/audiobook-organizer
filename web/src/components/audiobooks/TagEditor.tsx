// file: web/src/components/audiobooks/TagEditor.tsx
// version: 1.2.0
// guid: b3c4d5e6-f7a8-4b9c-0d1e-2f3a4b5c6d7e

import React, { useState, useRef } from 'react';
import {
  Chip,
  Autocomplete,
  TextField,
  Box,
  Popover,
  Button,
  Stack,
  Tooltip,
} from '@mui/material';
import { Add as AddIcon } from '@mui/icons-material';
import * as api from '../../services/api';
import { formatTagLabel, shouldRenderTagChip } from '../../utils/tagDisplay';

interface TagEditorProps {
  bookId: string;
  tags: string[];
  // detailedTags, when provided, carries per-tag source
  // attribution so user and system tags render differently.
  // Takes precedence over `tags` for display; `tags` is still
  // used for dedup/normalization checks during add.
  detailedTags?: api.DetailedBookTag[];
  allTags?: string[];
  onTagsChange?: (tags: string[]) => void;
  readOnly?: boolean;
  compact?: boolean;
}

// isSystemTag checks whether a tag's source marks it as
// server-applied provenance (not user-editable by default).
function isSystemTag(source: string | undefined): boolean {
  return source === 'system';
}

// systemTagCategory returns the top-level namespace of a system
// tag (e.g. "dedup", "metadata") so we can color-code by
// category. Unknown namespaces fall through to "default".
function systemTagCategory(tag: string): 'dedup' | 'metadata' | 'import' | 'organize' | 'default' {
  if (tag.startsWith('dedup:')) return 'dedup';
  if (tag.startsWith('metadata:')) return 'metadata';
  if (tag.startsWith('import:')) return 'import';
  if (tag.startsWith('organize:')) return 'organize';
  return 'default';
}

// systemTagColor maps a category to a MUI chip color. Soft
// outlined chips so system tags feel like "info" rather than
// "actions I can take".
function systemTagColor(tag: string): 'default' | 'primary' | 'secondary' | 'info' | 'warning' {
  switch (systemTagCategory(tag)) {
    case 'dedup':
      return 'warning';
    case 'metadata':
      return 'info';
    case 'import':
      return 'secondary';
    case 'organize':
      return 'primary';
    default:
      return 'default';
  }
}

export const TagEditor: React.FC<TagEditorProps> = ({
  bookId,
  tags,
  detailedTags,
  allTags = [],
  onTagsChange,
  readOnly = false,
  compact = false,
}) => {
  const [inputValue, setInputValue] = useState('');
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null);
  const addButtonRef = useRef<HTMLButtonElement>(null);

  // Build the render list — detailedTags when available, else
  // fall back to string-only tags (rendered as user tags).
  // Split into system and user groups so we can render them in
  // two rows with different styling.
  const effectiveTags: api.DetailedBookTag[] = detailedTags
    ? detailedTags
    : tags.map((t) => ({ tag: t, source: 'user', created_at: '' }));

  const userTags = effectiveTags.filter((t) => !isSystemTag(t.source));
  // metadata:source:* tags are filter-only (see FilterSidebar) — they never
  // render as chips here, same as on the book cards.
  const systemTags = effectiveTags.filter(
    (t) => isSystemTag(t.source) && shouldRenderTagChip(t.tag)
  );

  const availableSuggestions = allTags.filter(
    (t) => !tags.includes(t.toLowerCase())
  );

  const handleAddTag = async (tag: string) => {
    const normalized = tag.trim().toLowerCase();
    if (!normalized || tags.includes(normalized)) return;
    try {
      const updatedTags = await api.addBookUserTag(bookId, normalized);
      onTagsChange?.(updatedTags);
    } catch (_err) {
      console.error('Failed to add tag:', _err);
    }
    setInputValue('');
  };

  const handleRemoveTag = async (tag: string) => {
    try {
      const updatedTags = await api.removeBookUserTag(bookId, tag);
      onTagsChange?.(updatedTags);
    } catch (_err) {
      console.error('Failed to remove tag:', _err);
    }
  };

  // renderSystemChip and renderUserChip keep the two styles
  // separate. System chips are outlined, color-coded by
  // namespace, and have no delete action — the user can't
  // accidentally remove server-applied provenance.
  const renderSystemChip = (bt: api.DetailedBookTag) => (
    <Tooltip
      key={`sys:${bt.tag}`}
      title={`System tag (${bt.source}) — applied automatically by the server`}
    >
      <Chip
        label={formatTagLabel(bt.tag)}
        size="small"
        variant="outlined"
        color={systemTagColor(bt.tag)}
        sx={{ fontFamily: 'monospace', fontSize: '0.7rem' }}
      />
    </Tooltip>
  );

  const renderUserChip = (bt: api.DetailedBookTag) => (
    <Chip
      key={`usr:${bt.tag}`}
      label={bt.tag}
      size="small"
      variant="outlined"
      onDelete={readOnly ? undefined : () => handleRemoveTag(bt.tag)}
    />
  );

  const popoverOpen = Boolean(anchorEl);

  if (compact) {
    return (
      <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.5, alignItems: 'center' }}>
        {userTags.map(renderUserChip)}
        {systemTags.map(renderSystemChip)}
        {!readOnly && (
          <>
            <Button
              ref={addButtonRef}
              size="small"
              variant="text"
              sx={{ minWidth: 'auto', px: 0.5, fontSize: '0.75rem' }}
              onClick={(e) => setAnchorEl(e.currentTarget)}
            >
              <AddIcon sx={{ fontSize: 16 }} />
              Tag
            </Button>
            <Popover
              open={popoverOpen}
              anchorEl={anchorEl}
              onClose={() => {
                setAnchorEl(null);
                setInputValue('');
              }}
              anchorOrigin={{ vertical: 'bottom', horizontal: 'left' }}
            >
              <Box sx={{ p: 1, minWidth: 200 }}>
                <Autocomplete
                  freeSolo
                  size="small"
                  options={availableSuggestions}
                  inputValue={inputValue}
                  onInputChange={(_e, value) => setInputValue(value)}
                  onChange={(_e, value) => {
                    if (typeof value === 'string') {
                      handleAddTag(value);
                    }
                    setAnchorEl(null);
                  }}
                  renderInput={(params) => (
                    <TextField
                      {...params}
                      placeholder="Add tag..."
                      autoFocus
                      onKeyDown={(e) => {
                        if (e.key === 'Enter' && inputValue.trim()) {
                          e.preventDefault();
                          handleAddTag(inputValue);
                          setAnchorEl(null);
                        }
                      }}
                    />
                  )}
                />
              </Box>
            </Popover>
          </>
        )}
      </Box>
    );
  }

  return (
    <Stack spacing={1}>
      <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.5 }}>
        {userTags.map(renderUserChip)}
      </Box>
      {systemTags.length > 0 && (
        <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.5 }}>
          {systemTags.map(renderSystemChip)}
        </Box>
      )}
      {!readOnly && (
        <Autocomplete
          freeSolo
          size="small"
          options={availableSuggestions}
          inputValue={inputValue}
          onInputChange={(_e, value) => setInputValue(value)}
          onChange={(_e, value) => {
            if (typeof value === 'string') {
              handleAddTag(value);
            }
          }}
          renderInput={(params) => (
            <TextField
              {...params}
              placeholder="Add tag..."
              onKeyDown={(e) => {
                if (e.key === 'Enter' && inputValue.trim()) {
                  e.preventDefault();
                  handleAddTag(inputValue);
                }
              }}
            />
          )}
        />
      )}
    </Stack>
  );
};
