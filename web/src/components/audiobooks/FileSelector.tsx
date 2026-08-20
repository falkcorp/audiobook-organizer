// file: web/src/components/audiobooks/FileSelector.tsx
// version: 2.0.1
// guid: 8f2a3b4c-5d6e-7f8a-9b0c-1d2e3f4a5b6c

import { Box, Checkbox, Chip, MenuItem, Select, type SelectChangeEvent } from '@mui/material';
import type { BookSegment } from '../../services/api';

interface FileSelectorProps {
  segments: BookSegment[];
  selectedIds: Set<string>;
  onToggle: (id: string) => void;
  onSelectAll: () => void;
  onClearAll: () => void;
}

/**
 * Truncate a filename to maxLen characters, keeping the extension visible.
 */
function truncateBasename(filePath: string, maxLen = 26): string {
  const basename = filePath.split('/').pop() || filePath;
  if (basename.length <= maxLen) return basename;
  const extIdx = basename.lastIndexOf('.');
  const ext = extIdx >= 0 ? basename.slice(extIdx) : '';
  const nameOnly = extIdx >= 0 ? basename.slice(0, extIdx) : basename;
  const available = maxLen - ext.length - 1; // 1 for ellipsis char
  if (available <= 3) return basename.slice(0, maxLen - 1) + '\u2026';
  return nameOnly.slice(0, available) + '\u2026' + ext;
}

/**
 * Build chip label: zero-padded track number + truncated basename.
 */
function chipLabel(seg: BookSegment, totalDigits: number): string {
  const trackStr =
    seg.track_number != null ? String(seg.track_number).padStart(totalDigits, '0') + ' ' : '';
  return trackStr + truncateBasename(seg.file_path);
}

export const FileSelector = ({
  segments,
  selectedIds,
  onToggle,
  onSelectAll,
  onClearAll,
}: FileSelectorProps) => {
  // Hide for single-file books
  if (segments.length <= 1) return null;

  const totalDigits = Math.max(2, String(segments.length).length);
  const allSelected = selectedIds.size === segments.length;
  const noneSelected = selectedIds.size === 0;

  // > 20 segments: dropdown with multiple select
  if (segments.length > 20) {
    const selectedArray = Array.from(selectedIds);
    const handleChange = (event: SelectChangeEvent<string[]>) => {
      const val = event.target.value;
      const newIds = typeof val === 'string' ? val.split(',') : val;
      // Sync: toggle each difference
      const newSet = new Set(newIds);
      for (const id of segments.map((s) => s.id)) {
        const wasSelected = selectedIds.has(id);
        const isNowSelected = newSet.has(id);
        if (wasSelected !== isNowSelected) {
          onToggle(id);
        }
      }
    };

    return (
      <Box sx={{ mb: 2 }}>
        <Select
          size="small"
          multiple
          value={selectedArray}
          onChange={handleChange}
          sx={{ minWidth: 300 }}
          displayEmpty
          renderValue={(selected) =>
            selected.length === 0
              ? 'No files selected'
              : `${selected.length} file${selected.length === 1 ? '' : 's'} selected`
          }
        >
          {segments.map((seg) => (
            <MenuItem key={seg.id} value={seg.id}>
              <Checkbox size="small" checked={selectedIds.has(seg.id)} sx={{ p: 0, mr: 1 }} />
              {chipLabel(seg, totalDigits)}
            </MenuItem>
          ))}
        </Select>
      </Box>
    );
  }

  // 2-20 segments: horizontal scrollable chip strip with checkboxes
  return (
    <Box
      sx={{
        mb: 2,
        display: 'flex',
        flexWrap: 'nowrap',
        overflowX: 'auto',
        gap: 1,
        pb: 0.5,
        '&::-webkit-scrollbar': { height: 4 },
        '&::-webkit-scrollbar-thumb': { bgcolor: 'divider', borderRadius: 2 },
      }}
    >
      <Chip
        label={allSelected ? 'Clear' : 'Select All'}
        variant="outlined"
        color={allSelected ? 'default' : 'primary'}
        onClick={allSelected || !noneSelected ? onClearAll : onSelectAll}
      />
      {segments.map((seg) => {
        const isSelected = selectedIds.has(seg.id);
        return (
          <Chip
            key={seg.id}
            icon={
              <Checkbox
                size="small"
                checked={isSelected}
                sx={{ p: 0, '& .MuiSvgIcon-root': { fontSize: 16 } }}
              />
            }
            label={chipLabel(seg, totalDigits)}
            variant={isSelected ? 'filled' : 'outlined'}
            color={isSelected ? 'primary' : 'default'}
            onClick={() => onToggle(seg.id)}
            sx={{ flexShrink: 0 }}
          />
        );
      })}
    </Box>
  );
};
