// file: web/src/components/common/SignInRequiredNotice.tsx
// version: 0.1.0
// guid: d2c44b92-62e9-40ef-8cb3-3d1a220ca0e3
// last-edited: 2026-11-22

import { Alert, Link } from '@mui/material';
import { SIGN_IN_HELP_URL, SIGN_IN_REQUIRED_MESSAGE } from '../../utils/signInRequired';

/** The message that replaces a raw 403 on an action core refuses while authentication is off. */
export function SignInRequiredNotice({ sx }: { sx?: object }) {
  return (
    <Alert severity="info" sx={sx} data-testid="sign-in-required-notice">
      {SIGN_IN_REQUIRED_MESSAGE}.{' '}
      <Link href={SIGN_IN_HELP_URL} target="_blank" rel="noopener noreferrer">
        How to turn on sign-in
      </Link>
    </Alert>
  );
}
