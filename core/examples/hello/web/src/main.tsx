// file: core/examples/hello/web/src/main.tsx
// version: 0.1.0
// guid: 0e7b4d26-9a13-4f58-b6c1-3d8a5f2e0c94
// last-edited: 2026-11-20

import { createShell } from '@mediacore/shell';
import { helloModule } from './index';

createShell({ app: { id: 'hello', displayName: 'Hello' }, modules: [helloModule] }).mount('#root');
