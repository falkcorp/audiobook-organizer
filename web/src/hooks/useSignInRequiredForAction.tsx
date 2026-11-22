// file: web/src/hooks/useSignInRequiredForAction.tsx
// version: 0.1.0
// guid: 0ac24b65-4f44-4eab-9088-557db0e23b6a
// last-edited: 2026-11-22

import { useCallback, useState, type ReactNode } from 'react';
import { useOptionalAuth } from '../contexts/AuthContext';
import { SignInRequiredNotice } from '../components/common/SignInRequiredNotice';
import { isAuthOff, isSignInRequiredRefusal } from '../utils/signInRequired';

export interface SignInRequiredGate {
  /** True when the action must not be offered: auth is off, or the server just refused it. */
  blocked: boolean;
  /** Call from a catch: true when `err` was the refusal (now shown as the notice), false for any other error. */
  refuse: (err: unknown) => boolean;
  /** The notice to render next to the action while blocked, else null. */
  notice: ReactNode;
}

/** One rule for the 11 SEC5-8 actions: disable up front under auth-off, and map the 403 if the call goes out anyway. */
export function useSignInRequiredForAction(): SignInRequiredGate {
  const auth = useOptionalAuth();
  const authOff = isAuthOff(auth?.authEnabled, auth?.initialized);
  const [refused, setRefused] = useState(false);
  const refuse = useCallback(
    (err: unknown) => {
      if (!isSignInRequiredRefusal(err, authOff)) return false;
      setRefused(true);
      return true;
    },
    [authOff]
  );
  const blocked = authOff || refused;
  return { blocked, refuse, notice: blocked ? <SignInRequiredNotice /> : null };
}
