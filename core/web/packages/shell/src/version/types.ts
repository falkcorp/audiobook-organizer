// file: core/web/packages/shell/src/version/types.ts
// version: 0.1.0
// guid: 20f807b0-6352-4a80-9d00-4fced001a9b9
// last-edited: 2026-11-20

// The /api/core/v1/modules body (07 §7.5.1; Go internal/modulehttp) and the
// intersection result (07 §7.5.2).

import type { ModuleUI } from '../types';

export interface ServerModuleUI {
  pages: readonly string[];
  nav: readonly string[];
  settings_panels: readonly string[];
  widgets: readonly string[];
  banners: readonly string[];
  top_bar_items: readonly string[];
  user_page: readonly string[];
  activity_panes: readonly string[];
  setup_steps: readonly string[];
}

export interface ServerModule {
  id: string;
  version: string;
  display_name: string;
  ui: ServerModuleUI;
}

export interface ServerModules {
  core: { version: string };
  app: { id: string; display_name: string; version: string; ui_build_id: string };
  restart_required: boolean;
  modules: readonly ServerModule[];
  /** Present only for callers holding core:admin. */
  disabled_modules?: readonly string[];
}

export type DiagnosticKind =
  | 'core-version-incompatible'
  | 'ui-missing'
  | 'version-incompatible'
  | 'undeclared-descriptor'
  | 'module-not-enabled'
  | 'descriptor-without-ui'
  // The server's app.id differs from the page's app ID (07 §7.10, FE7-8).
  | 'app-id-mismatch';

export interface Diagnostic {
  kind: DiagnosticKind;
  module?: string;
  /** `<slot>:<id>` for item-level kinds, e.g. `nav:gadgets`. */
  item?: string;
  detail: string;
  /** `debug` entries are for the debug log only and are never reported. */
  level?: 'debug';
}

export interface IntersectResult {
  blocked: null | { kind: 'core-version-incompatible'; client: string; server: string };
  /** The modules to mount, with every undeclared item dropped. */
  modules: ModuleUI[];
  diagnostics: Diagnostic[];
}
