// file: web/src/components/audiobooks/ColumnChooser.tsx
// version: 1.0.1
// guid: d4e5f6a7-b8c9-4d0e-1f2a-3b4c5d6e7f80

import React, { useState } from 'react';
import {
  Popover,
  Button,
  Box,
  Typography,
  Checkbox,
  FormControlLabel,
  Divider,
} from '@mui/material';
import { ViewColumn as ViewColumnIcon } from '@mui/icons-material';
import { COLUMN_CATEGORIES, getColumnsByCategory } from '../../config/columnDefinitions';

interface ColumnChooserProps {
  visibleColumnIds: string[];
  onToggleColumn: (columnId: string) => void;
  onResetDefaults: () => void;
}

export const ColumnChooser: React.FC<ColumnChooserProps> = ({
  visibleColumnIds,
  onToggleColumn,
  onResetDefaults,
}) => {
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null);
  const open = Boolean(anchorEl);

  const grouped = getColumnsByCategory();
  const visibleSet = new Set(visibleColumnIds);

  return (
    <>
      <Button
        startIcon={<ViewColumnIcon />}
        onClick={(e) => setAnchorEl(e.currentTarget)}
        variant="outlined"
        size="small"
      >
        Columns
      </Button>
      <Popover
        open={open}
        anchorEl={anchorEl}
        onClose={() => setAnchorEl(null)}
        anchorOrigin={{ vertical: 'bottom', horizontal: 'left' }}
        transformOrigin={{ vertical: 'top', horizontal: 'left' }}
      >
        <Box sx={{ p: 2, minWidth: 240, maxHeight: 480, overflow: 'auto' }}>
          {COLUMN_CATEGORIES.map((category, catIdx) => {
            const cols = grouped[category];
            if (!cols || cols.length === 0) return null;
            return (
              <React.Fragment key={category}>
                {catIdx > 0 && <Divider sx={{ my: 1 }} />}
                <Typography
                  variant="caption"
                  sx={{
                    color: 'text.secondary',
                    fontWeight: 'bold',
                    display: 'block',
                    mb: 0.5,
                  }}
                >
                  {category}
                </Typography>
                {cols.map((col) => (
                  <FormControlLabel
                    key={col.id}
                    sx={{ display: 'block', ml: 0 }}
                    control={
                      <Checkbox
                        size="small"
                        checked={visibleSet.has(col.id)}
                        onChange={() => onToggleColumn(col.id)}
                      />
                    }
                    label={<Typography variant="body2">{col.label}</Typography>}
                  />
                ))}
              </React.Fragment>
            );
          })}
          <Divider sx={{ my: 1 }} />
          <Button size="small" onClick={onResetDefaults} fullWidth>
            Reset to Defaults
          </Button>
        </Box>
      </Popover>
    </>
  );
};
