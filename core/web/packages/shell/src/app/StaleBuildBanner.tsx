// file: core/web/packages/shell/src/app/StaleBuildBanner.tsx
// version: 0.1.0
// guid: 4d8a2f65-1c39-4e70-a5b3-7f0e9c6d2b18
// last-edited: 2026-11-20

import { useEffect, useState } from 'react';
import { Alert, Button } from '@mui/material';
import { onStaleBuild, type StaleReason } from '@mediacore/api-client';

/**
 * The one "new version — reload" banner (07 §7.5.2, FE2-5): not
 * dismissable, fed by the api-client's single stale-build rule, which
 * replays reasons detected before the banner mounted (FE8-3).
 */
export function StaleBuildBanner() {
  const [reason, setReason] = useState<StaleReason | null>(null);
  useEffect(() => onStaleBuild((r) => setReason((prev) => prev ?? r)), []);
  if (!reason) return null;
  return (
    <Alert
      severity="info"
      role="alert"
      data-testid="stale-build-banner"
      data-reason={reason}
      action={<Button color="inherit" size="small" onClick={() => window.location.reload()}>Reload</Button>}
      sx={{ borderRadius: 0 }}
    >
      A new version of this app is available.
    </Alert>
  );
}
