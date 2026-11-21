// file: web/src/components/users/IdentityList.tsx
// version: 0.1.0
// guid: b7b7fa4c-5f81-4b60-85ce-b361aed028c1
// last-edited: 2026-11-21

// The linked-sign-ins list shown in the footprint drawer and the merge preview (F9-04 slice 4).

import { Box, Chip, Typography } from '@mui/material';
import type { UserMergeIdentity } from '../../services/api';

export function IdentityList({ identities }: { identities: UserMergeIdentity[] }) {
  if (identities.length === 0) {
    return (
      <Typography variant="body2" color="text.secondary">
        No linked sign-ins
      </Typography>
    );
  }
  return (
    <Box component="ul" sx={{ m: 0, pl: 2 }}>
      {identities.map((id) => (
        <li key={id.id}>
          <Typography variant="body2" component="span">
            {id.provider}
            {id.email ? ` · ${id.email}` : ''} · {id.subject}
          </Typography>
          {id.owner_email && <Chip size="small" color="warning" label="owner" sx={{ ml: 0.5 }} />}
        </li>
      ))}
    </Box>
  );
}
