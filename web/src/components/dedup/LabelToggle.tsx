// file: web/src/components/dedup/LabelToggle.tsx
// version: 1.0.0
// guid: 6f1b8d39-4a27-4c50-9e83-1d7a5c0b2e64
// last-edited: 2026-06-19

// LabelToggle is the 3-way segmented control for a dedup gold label:
// [ Dup | Unsure | Not ]. The current label is highlighted (green / amber /
// red). Selecting a value calls onChange; clicking the already-selected value
// is a no-op (the override would be redundant).

import { ToggleButton, ToggleButtonGroup } from '@mui/material';

export type DedupLabelValue = 'true_dup' | 'unsure' | 'not_dup' | '';

interface LabelToggleProps {
  value: DedupLabelValue | string;
  onChange: (label: DedupLabelValue) => void;
  disabled?: boolean;
  size?: 'small' | 'medium';
}

const OPTIONS: Array<{ value: DedupLabelValue; label: string; color: string }> = [
  { value: 'true_dup', label: 'Dup', color: 'success.main' },
  { value: 'unsure', label: 'Unsure', color: 'warning.main' },
  { value: 'not_dup', label: 'Not', color: 'error.main' },
];

export function LabelToggle({ value, onChange, disabled, size = 'small' }: LabelToggleProps) {
  return (
    <ToggleButtonGroup
      exclusive
      size={size}
      value={value || null}
      disabled={disabled}
      onChange={(_, next) => {
        // exclusive returns null when the selected button is re-clicked; ignore.
        if (next) onChange(next as DedupLabelValue);
      }}
      aria-label="dedup label"
    >
      {OPTIONS.map((o) => (
        <ToggleButton
          key={o.value}
          value={o.value}
          aria-label={o.label}
          sx={{
            px: 1.5,
            fontWeight: 600,
            textTransform: 'none',
            '&.Mui-selected': {
              color: 'common.white',
              bgcolor: o.color,
              '&:hover': { bgcolor: o.color, filter: 'brightness(0.92)' },
            },
          }}
        >
          {o.label}
        </ToggleButton>
      ))}
    </ToggleButtonGroup>
  );
}
