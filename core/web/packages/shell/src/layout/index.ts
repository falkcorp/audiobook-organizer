// file: core/web/packages/shell/src/layout/index.ts
// version: 0.1.0
// guid: 85929ef2-9b2f-407d-a8db-ecae4e535add
// last-edited: 2026-11-21

// The shell's layout (the layout slots, TopBar, UserMenu, the sidebar, the
// operations indicator). F9-02 fills this entry file; F9-05 created it so the
// shell root re-exports it. Each frame takes its data and its audiobook (or any
// module's) pieces as props and slots; the audiobook app's layout directory
// binds the books ones.

export { isSubItemSelected } from './sidebarSelection';
export { SidebarFrame, type SidebarFrameProps, type SidebarItem } from './SidebarFrame';
export {
  TopBarFrame,
  type ConnectionFeed,
  type ConnectionState,
  type TopBarFrameProps,
} from './TopBarFrame';
export { MainLayoutFrame, type MainLayoutFrameProps } from './MainLayoutFrame';
export { UserMenuFrame, type UserMenuFrameProps } from './UserMenuFrame';
export {
  OperationsIndicatorFrame,
  type ActivityTarget,
  type OperationsFeed,
  type OperationsFrameRow,
  type OperationsIndicatorFrameProps,
  type RunStatusModel,
} from './OperationsIndicatorFrame';
export { formatProgressCounts } from './operationProgress';

export { UserMenu } from './UserMenu';
export {
  OperationsIndicator,
  shellRunStatus,
  useShellOperationsFeed,
  type OperationsIndicatorProps,
} from './shellOperations';
