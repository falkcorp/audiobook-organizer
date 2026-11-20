// file: core/examples/hello/web/src/index.ts
// version: 0.1.0
// guid: 6f2a9e41-0c75-4b38-9d16-e4b7c1a3f580
// last-edited: 2026-11-20

import { defineModule } from '@mediacore/shell';
import WavingHandIcon from '@mui/icons-material/WavingHand';
import { manifest } from './manifest.gen'; // generated from Manifest(): 07 §7.4.1

// 06 §6.12.4, less the 'hello:read' / 'hello:admin' permissions: hello's
// Manifest declares no Permissions yet (module.Manifest.Permissions has no
// owning row), so the generated list is empty and Perm<typeof manifest>
// admits only core:* names. The permission and permissionsHints entries
// return when the manifest gains them.
export const helloModule = defineModule(manifest, {
  routes: [{ id: 'greeting', path: 'greeting', lazy: () => import('./GreetingPage'), title: 'Greeting' }],
  nav: [{ id: 'hello', label: 'Hello', icon: WavingHandIcon, to: 'greeting', goKey: 'e' }],
  settingsPanels: [{ id: 'hello', label: 'Hello', lazy: () => import('./HelloSettings') }],
  permissionsHints: { view: [] },
});
