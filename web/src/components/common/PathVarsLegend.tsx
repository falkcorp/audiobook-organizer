// file: web/src/components/common/PathVarsLegend.tsx
// version: 1.0.0
// guid: 8b4e0d27-1c93-4a56-9f70-2e6a5c8d1b34
// last-edited: 2026-06-19

// PathVarsLegend renders the expansion key for abbreviated paths, e.g.
//   Information: $(libroot) = /mnt/bigdata/books/audiobook-organizer · $(books) = /mnt/bigdata/books
// Place at the bottom of any page that shows abbreviated paths.

import { Typography } from '@mui/material';
import { usePathVars, type PathVar } from '../../utils/formatPath';

interface PathVarsLegendProps {
  /** Override the vars (defaults to the shared config-derived vars). */
  vars?: PathVar[];
}

export function PathVarsLegend({ vars }: PathVarsLegendProps) {
  const loaded = usePathVars();
  const list = vars ?? loaded;
  if (list.length === 0) return null;
  return (
    <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 2 }}>
      Information:{' '}
      {list.map((v, i) => (
        <span key={v.name}>
          {i > 0 && ' · '}
          <code>{`$(${v.name})`}</code> = {v.value}
        </span>
      ))}
    </Typography>
  );
}
