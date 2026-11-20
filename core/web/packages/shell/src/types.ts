// file: core/web/packages/shell/src/types.ts
// version: 0.1.0
// guid: 5d9b3f60-2e81-4a47-bc15-8f0a6c2e7d93
// last-edited: 2026-11-20

// The extension API types of 07 §7.4.2, copied verbatim from the spec. The
// hooks and entry points it declares live beside their implementations and
// are re-exported from index.ts.

import type { MediaThemeInput } from '@mediacore/ui-kit';
import type { ComponentType, ReactNode } from 'react';
import type { SvgIconProps } from '@mui/material';
import type { ThemeOptions } from '@mui/material/styles';

export type Permission = `${string}:${string}`;

export interface ModuleManifest {
  readonly id: string;
  readonly version: string;
  readonly displayName: string;
  readonly pages: readonly string[];
  readonly nav: readonly string[];
  readonly settingsPanels: readonly string[];
  readonly widgets: readonly string[];
  readonly banners: readonly string[];
  readonly topBarItems: readonly string[];
  readonly userPage: readonly string[];
  readonly activityPanes: readonly string[];
  readonly setupSteps: readonly string[];
  readonly permissions: readonly Permission[];
}

export type Lazy = () => Promise<{ default: ComponentType }>;
export type Icon = ComponentType<SvgIconProps>;
export type PageId<M extends ModuleManifest> = M['pages'][number];
export type Perm<M extends ModuleManifest> = M['permissions'][number] | `core:${string}`;

export interface RouteContribution<M extends ModuleManifest> {
  id: PageId<M>;
  path: string;            // relative to /<module id>/, e.g. 'library/:id'
  lazy: Lazy;              // code-split; wrapped in Suspense + ErrorBoundary by the shell
  permission?: Perm<M>;    // advisory guard: renders "no access" instead of the page
  title?: string;
}

export interface NavContribution<M extends ModuleManifest> {
  id: M['nav'][number];
  label: string;
  icon?: Icon;
  to: PageId<M> | { page: PageId<M>; search?: string };
  permission?: Perm<M>;    // hidden when the user lacks it
  order?: number;
  // A React hook (FE-22): must be named use*, have stable identity, and obey
  // the rules of hooks. The shell calls it inside a dedicated <NavBadge>
  // component per entry, so the React Compiler and rules-of-hooks accept it.
  useBadge?: () => number | undefined;
  // One lowercase letter: the chord "g <goKey>" navigates to this entry. The
  // shell builds ui-kit's useKeyboardShortcuts table from its own routes
  // ("g s" -> /settings) plus these, so no ui-kit hook names a module route
  // (books' library entry sets 'l', HEAD's "g l"; FE8-5). A key the shell or
  // an earlier module (ID order) already uses is dropped with a console warning.
  goKey?: string;
  children?: readonly NavContribution<M>[];
}

export interface SettingsPanelContribution<M extends ModuleManifest> {
  id: M['settingsPanels'][number];
  label: string;
  lazy?: Lazy;             // a module panel, or omit and pass schemaSection for the schema form (§7.4.5)
  schemaSection?: string;  // config section path, e.g. 'modules.hello'
  permission?: Perm<M>;
  order?: number;
}

export interface PermissionsHints<M extends ModuleManifest> {
  view: readonly Perm<M>[];                 // any-of: needed to show the module at all
  actions?: Readonly<Record<string, Perm<M>>>; // named UI actions -> permission, for useAction()
}

export interface WidgetContribution<M extends ModuleManifest> {
  id: M['widgets'][number];
  placement: 'home' | 'system' | 'diagnostics';
  lazy: Lazy;
  permission?: Perm<M>;
  order?: number;
}

export interface BannerContribution<M extends ModuleManifest> {
  id: M['banners'][number];
  lazy: Lazy;              // rendered by the shell's main layout above page content; renders null when idle
  permission?: Perm<M>;
  order?: number;
}

export interface TopBarContribution<M extends ModuleManifest> {
  id: M['topBarItems'][number];
  lazy: Lazy;
  permission?: Perm<M>;
  order?: number;
}

export interface UserPageExtensions<M extends ModuleManifest> {
  tabs?: readonly { id: M['userPage'][number]; label: string; lazy: Lazy; permission?: Perm<M> }[];
  rowActions?: readonly {
    id: M['userPage'][number];
    label: string;
    lazy: () => Promise<{ default: ComponentType<{ userId: string; onDone: () => void }> }>;
    permission?: Perm<M>;
  }[];
}

export interface ActivityPaneContribution<M extends ModuleManifest> {
  id: M['activityPanes'][number];
  label: string;
  lazy: Lazy;
  permission?: Perm<M>;
  order?: number;
}

export interface SetupContribution<M extends ModuleManifest> {
  // Asked after login (and on /setup) whether this module still needs first-run
  // setup; books: !config.root_dir && !config.setup_complete (App.tsx:87-89).
  isRequired: () => Promise<boolean>;
  steps: readonly { id: M['setupSteps'][number]; title: string; lazy: Lazy }[];
}

export interface TaskKindPresentation {
  label: string;
  color?: 'default' | 'primary' | 'secondary' | 'info' | 'success' | 'warning' | 'error';
  resultView?: Lazy; // renders a finished run's result in the Activity tasks pane
}

export interface ModuleUIInput<M extends ModuleManifest> {
  routes: readonly RouteContribution<M>[];
  nav: readonly NavContribution<M>[];
  settingsPanels: readonly SettingsPanelContribution<M>[];
  permissionsHints: PermissionsHints<M>;
  // optional
  widgets?: readonly WidgetContribution<M>[];
  banners?: readonly BannerContribution<M>[];
  topBarItems?: readonly TopBarContribution<M>[];
  userPageExtensions?: UserPageExtensions<M>;
  activityPanes?: readonly ActivityPaneContribution<M>[];
  setup?: SetupContribution<M>;
  themeExtend?: ThemeOptions;                                         // merged by the shell, per module (§7.7)
  taskKinds?: Readonly<Record<`${M['id']}.${string}`, TaskKindPresentation>>;
  // No redirects field: the shell redirects no former page path (D13, D10);
  // a former bookmark lands on NotFound (§7.19 step 5).
  providers?: readonly ComponentType<{ children: ReactNode }>[];     // module-wide contexts
}

export interface ModuleUI<M extends ModuleManifest = ModuleManifest> extends ModuleUIInput<M> {
  readonly id: M['id'];
  readonly version: M['version'];
  readonly manifest: M;
}

export interface ShellConfig {
  // app.id must equal the preset's <meta name="mediacore-app"> (the one
  // app-ID source, §7.10); createShell refuses to mount on a difference
  // (FE7-8).
  app: { id: string; displayName: string; logo?: ComponentType };
  modules: readonly ModuleUI[];
  // No defaultMode here: the default mode and the mode key come from the
  // preset's <meta name="mediacore-theme"> (§7.7), the one source the boot
  // script also reads (FE6-2).
  theme?: Omit<MediaThemeInput, 'defaultMode'>;
  // No pre-shell storage-key option (D13): every shell preference is a
  // storageKey() name (§7.7), and no localStorage key that predates the shell
  // is read. ui-kit holds no key name: ConfigurableTable takes its full key
  // from its caller. The theme-mode key is the preset's themeStorageKey
  // (§7.10, FE6-2), not a createShell option.
}

export interface Shell {
  mount(target: string | Element): void;
  unmount(): void;
}
