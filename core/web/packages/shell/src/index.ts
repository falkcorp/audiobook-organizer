// file: core/web/packages/shell/src/index.ts
// version: 0.1.0
// guid: 9b4e1f27-6c03-4a58-8d92-e0a7c5b3f146
// last-edited: 2026-11-20

// @mediacore/shell: createShell, defineModule and the hooks module code uses
// (07 §7.4.2). /testing and /vite are separate entry points.

export type * from './types';
export type { AuthState, AuthUser, AuthStatus, TaskRunView, TaskRunState, ToastApi, ToastSeverity } from './contracts';
export { createShell } from './app/createShell';
export { defineModule } from './defineModule';
export { setShellAppId, storageKey } from './app/appId';
export { useApi } from './app/context';
export { useAction, useAuth, usePermission } from './auth';
export { useResync, useTopic } from './realtime';
export { useToast } from './toast';
export { useTask, useTaskError } from './tasks';
export { SchemaForm, type SchemaFormProps } from './settings';
export { shellReservedSegments } from './routes';
export * from './providers';
export * from './layout';
export * from './pages';
