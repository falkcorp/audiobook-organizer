// file: core/web/packages/shell/test/setup.ts
// version: 0.1.0
// guid: 6b0e3d52-9a18-4c7f-b245-e8d1f7a0c369
// last-edited: 2026-11-20

import '@testing-library/jest-dom/vitest';
import { cleanup, configure } from '@testing-library/react';
import { afterEach } from 'vitest';

// findBy*/waitFor resolve the moment their condition holds, so a longer ceiling
// costs a passing test nothing. The 1s default timed out the SettingsSection
// retry test (it waits for a MUI form to mount after a refetch) under CPU
// contention, on a run recorded in the r1 review of F9-01.
configure({ asyncUtilTimeout: 5000 });

afterEach(() => {
  cleanup();
  localStorage.clear();
  sessionStorage.clear();
});
