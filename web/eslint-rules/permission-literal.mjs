// file: web/eslint-rules/permission-literal.mjs
// version: 0.1.0
// guid: 3d7a1e52-9c40-4b86-a2f1-6e08c5b97d14
// last-edited: 2026-11-21

// The permission-literal rule of 07 section 7.18 (FE-11, FE2-17): the UI never
// hard-codes a permission string. It is an AST rule (no-restricted-syntax),
// not a lexical grep: a grep for dotted strings matches 744 MUI palette keys
// and legacy event names.
//
// It fails on a string literal (or a template literal with no substitution)
//   - passed to usePermission, useAction or hasPermission (by name, or as a
//     method of that name), and to HEAD's own names for the same decision,
//     useHasPermission and holdsPermission;
//   - assigned to a `permission` property or JSX attribute, or to a property or
//     constant named `*_PERMISSION`;
//   - placed in an API-key scope list: a `scopes` property or a constant named
//     `*_SCOPES` holding an array of literals.
// A permission that comes from `manifest.permissions` or from the generated
// client is an identifier or member expression, which none of these selectors
// match, so it passes.

const PERMISSION_CALLEES = ['usePermission', 'useAction', 'hasPermission', 'useHasPermission', 'holdsPermission'];
const CALLEE = PERMISSION_CALLEES.join('|');
const PERMISSION_NAME = '/^(permission|[A-Z0-9_]*_PERMISSION)$/';
const SCOPES_NAME = '/^(scopes|[A-Z0-9_]*_SCOPES)$/';

const MESSAGE =
  'A permission is never a string literal here (07 section 7.18): take it from manifest.permissions or the generated @mediacore/api-client.';

// A string in any of its two literal shapes. Literal[value=/./] would also
// match a number or a regexp, so the type is spelled out.
const STRING_LITERAL = "Literal[value=/^/][raw=/^[\"']/]";
const PLAIN_TEMPLATE = 'TemplateLiteral[expressions.length=0]';
const LITERAL = `:matches(${STRING_LITERAL}, ${PLAIN_TEMPLATE})`;

export const PERMISSION_LITERAL_SELECTORS = [
  // usePermission('x'), obj.hasPermission('x'), holdsPermission(auth, 'x')
  `CallExpression[callee.name=/^(${CALLEE})$/] > ${LITERAL}.arguments`,
  `CallExpression[callee.property.name=/^(${CALLEE})$/] > ${LITERAL}.arguments`,
  // { permission: 'x' }, { LIBRARY_DELETE_PERMISSION: 'x' }, { 'permission': 'x' }
  `Property[key.name=${PERMISSION_NAME}] > ${LITERAL}.value`,
  `Property[key.value=${PERMISSION_NAME}] > ${LITERAL}.value`,
  // const X_PERMISSION = 'x'
  `VariableDeclarator[id.name=${PERMISSION_NAME}] > ${LITERAL}.init`,
  // <Gate permission="x" />
  `JSXAttribute[name.name=${PERMISSION_NAME}] > ${LITERAL}.value`,
  `JSXAttribute[name.name=${PERMISSION_NAME}] > JSXExpressionContainer > ${LITERAL}.expression`,
  // { scopes: ['x'] }, const ALL_SCOPES = ['x']
  `Property[key.name=${SCOPES_NAME}] > ArrayExpression > ${LITERAL}.elements`,
  `VariableDeclarator[id.name=${SCOPES_NAME}] > ArrayExpression > ${LITERAL}.elements`,
];

/** The no-restricted-syntax option list: one entry per selector. */
export const permissionLiteralRestrictions = PERMISSION_LITERAL_SELECTORS.map((selector) => ({
  selector,
  message: MESSAGE,
}));
