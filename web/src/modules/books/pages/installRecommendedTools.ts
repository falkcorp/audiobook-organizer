// file: web/src/modules/books/pages/installRecommendedTools.ts
// version: 0.1.0
// guid: 45dcce3a-0609-4fc7-bc44-6020695e5221
// last-edited: 2026-11-21

// The first-run wizard's "Install recommended tools" choice: fpcalc (fingerprinting, ~2 MB) and
// Ollama (local AI, ~5 GB), each through POST /api/v1/tools/:name/install. That route downloads
// synchronously, so this runs after the setup is saved and the wizard has closed, never inside it.

import * as api from '../../../services/api';
import { SIGN_IN_REQUIRED_MESSAGE, isSignInRequiredRefusal } from '../../../utils/signInRequired';

/** Smallest first, so a failed Ollama download does not hold back fpcalc. */
export const RECOMMENDED_TOOLS = ['fpcalc', 'ollama'] as const;

const signInRequiredToolsMessage = `${SIGN_IN_REQUIRED_MESSAGE}. Install the recommended tools from Settings, Tools once sign-in is on.`;

type Notify = (message: string, severity?: 'success' | 'error' | 'warning' | 'info') => void;

/**
 * Installs each recommended tool in turn and reports through `notify`. A tool the server says is
 * already installed (409) counts as installed. A failure never stops the next tool and is never
 * thrown: the setup is already saved, so the user is told which tool failed and where to retry.
 *
 * Under auth-off (`authOff`) core refuses the install (SEC5-8): nothing is attempted, and a refusal
 * that arrives anyway stops the loop; either way the user is told to turn on sign-in, not shown a 403.
 */
export async function installRecommendedTools(notify: Notify, authOff = false): Promise<void> {
  if (authOff) {
    notify(signInRequiredToolsMessage, 'warning');
    return;
  }
  notify(
    'Installing recommended tools in the background (Ollama is about 5 GB, so this can take a while).',
    'info'
  );
  const failures: string[] = [];
  for (const name of RECOMMENDED_TOOLS) {
    try {
      await api.installTool(name);
    } catch (error) {
      if (error instanceof api.ApiError && error.status === 409) continue;
      if (isSignInRequiredRefusal(error, authOff)) {
        notify(signInRequiredToolsMessage, 'warning');
        return;
      }
      const reason = error instanceof Error && error.message ? error.message : 'unknown error';
      failures.push(`${name} (${reason})`);
    }
  }
  if (failures.length === 0) {
    notify('Recommended tools are installed.', 'success');
    return;
  }
  notify(
    `Could not install ${failures.join(', ')}. Setup is saved; install it from Settings, Tools.`,
    'error'
  );
}
