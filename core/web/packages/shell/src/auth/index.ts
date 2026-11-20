// file: core/web/packages/shell/src/auth/index.ts
// version: 0.1.0
// guid: 465d4650-a0b5-422e-9bf3-c5f9774e813d
// last-edited: 2026-11-20

export { AuthProvider, type AuthProviderProps } from './AuthProvider';
export { AuthClientContext, AuthContext, ModulesHintsContext } from './context';
export { grantsFor, grantsOf, hasPermission, useAction, useAuth, useAuthClient, useGrants, usePermission, type Grants } from './hooks';
export { RequireAuth } from './RequireAuth';
export { safeReturn } from './safeReturn';
export { default as InvitePage } from './InvitePage';
export { default as LoginPage } from './LoginPage';
export { default as SetupPage } from './SetupPage';
export { default as TempLoginPage } from './TempLoginPage';
