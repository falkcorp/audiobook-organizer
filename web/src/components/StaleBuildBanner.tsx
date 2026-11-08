// file: web/src/components/StaleBuildBanner.tsx
// version: 1.0.0
// guid: d406b9f4-517f-42bd-a224-bff3843a1eeb
// last-edited: 2026-11-08

import { useEffect, useState } from 'react';
import { Alert, Button, Snackbar } from '@mui/material';
import { getStaleReasons, onStaleBuild } from '../utils/apiFetch';

/**
 * The reload banner (07 §7.5.2; 09 §9.4 R1): after an upgrade, an open tab
 * still runs the old bundle, whose lazy chunks the new server no longer has.
 * apiFetch's build-ID rule notes the first response that proves it (its
 * X-App-Build differs from this page's mediacore-build meta), and this offers
 * the reload. It is not dismissable: the tab cannot be trusted to keep working,
 * and there is one rule and one offer.
 *
 * It subscribes through onStaleBuild, which replays what was already detected,
 * so it shows even when it mounts after the auth probe's response. Mount it
 * once, in App. It is fixed-position, so it changes no layout and shows over
 * the login page and the app alike.
 */
export function StaleBuildBanner() {
  const [stale, setStale] = useState(() => getStaleReasons().length > 0);

  useEffect(() => onStaleBuild(() => setStale(true)), []);

  if (!stale) return null;

  return (
    <Snackbar open anchorOrigin={{ vertical: 'top', horizontal: 'center' }}>
      <Alert
        severity="info"
        variant="filled"
        action={
          <Button color="inherit" size="small" onClick={() => window.location.reload()}>
            Reload
          </Button>
        }
      >
        A new version is available. Reload to use it.
      </Alert>
    </Snackbar>
  );
}
