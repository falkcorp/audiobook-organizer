// file: core/web/packages/shell/test/setup.ts
// version: 0.1.0
// guid: 6b0e3d52-9a18-4c7f-b245-e8d1f7a0c369
// last-edited: 2026-11-20

import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach } from 'vitest';

afterEach(() => {
  cleanup();
  localStorage.clear();
  sessionStorage.clear();
});
