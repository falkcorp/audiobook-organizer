// file: web/src/components/setup/setupTypes.ts
// version: 0.1.0
// guid: 4b03b2c3-b9db-42bd-a9c1-d243aa2bdad8
// last-edited: 2026-11-21

// F9-04 slice 5: ABO's local, richer twin of the shell's SetupContribution (07 §7.4.2); the gaps
// against the typed shape are in seams.d/F9-04-s5-setup.md.

import type { ComponentType } from 'react';

export interface SetupStep {
  id: string;
  title: string;
  /** Rendered while the step is active. `onNext` moves to the following step (a step can offer Skip). */
  Component: ComponentType<{ onNext: () => void }>;
}

export interface SetupContribution {
  isRequired: () => Promise<boolean>;
  steps: readonly SetupStep[];
}
