// file: web/src/components/activity/CustomCompactDaysField.tsx
// version: 1.0.0
// guid: 7a1f3d92-6c4e-4b85-9e20-3f8b5d1c7a46
// last-edited: 2026-09-13

import { useState } from 'react';
import { TextField } from '@mui/material';
import { COMPACT_DAYS_ERROR, parseCompactDays } from './compactDays';

interface CustomCompactDaysFieldProps {
  value: string;
  onChange: (value: string) => void;
  /** Called only with a validated whole number of days (>= 1). */
  onSubmit: (days: number) => void;
}

/**
 * The "Custom days" box in the Activity Log Compact menu (rendered in both the
 * mobile and desktop toolbars). Enter submits; an invalid value shows an
 * inline error and never reaches onSubmit. Editing clears the error.
 */
export default function CustomCompactDaysField({
  value,
  onChange,
  onSubmit,
}: CustomCompactDaysFieldProps) {
  const [invalid, setInvalid] = useState(false);

  return (
    <TextField
      size="small"
      type="number"
      placeholder="Custom days"
      value={value}
      error={invalid}
      helperText={invalid ? COMPACT_DAYS_ERROR : undefined}
      onChange={(e) => {
        setInvalid(false);
        onChange(e.target.value);
      }}
      onKeyDown={(e) => {
        if (e.key === 'Enter') {
          const days = parseCompactDays(value);
          if (days === null) {
            setInvalid(true);
          } else {
            onSubmit(days);
          }
        }
        e.stopPropagation();
      }}
      onClick={(e) => e.stopPropagation()}
      sx={{ width: 160 }}
      slotProps={{
        input: { inputProps: { min: 1, step: 1, 'aria-label': 'Custom days' } },
      }}
    />
  );
}
