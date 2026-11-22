### Fixed

#### Core identity: API keys, Access sign-ins and owner routes answer as they did before the cutover

Under core identity a set of requests were refused that the legacy build
admitted (F4-08 B10y):

- An Access (Cloudflare) sign-in is again a credential-changing method, so
  an Access-verified admin passes the credential routes as before.
- Guards read a method's registered classification through the principal,
  so an extension's sign-in is classified by what it declares.
- `playlists.create` carries a real route requirement. Its empty one made
  the atom unholdable, which broke every "holds every atom" check (an
  admin's key on admin routes, approving a Repairs run of an unregistered
  fixer).
- Books' invite list and delete take `users.manage` through any method,
  an API key included, and work with authentication off, as before. Create
  and resend still need an interactive sign-in. Core's own invite routes
  are unchanged.
- The protected configuration keys again need the admin role by name, an
  active account, a key that may act as admin, and `core:admin`.
- Backup restore, the user-merge routes and the guest-service-token routes
  demand the deployment owner only while an owner e-mail is set. With none
  set they keep their ordinary guards again: the route's permission and a
  credential-changing sign-in (a key and auth-off are still refused). Core's
  own owner routes keep requiring `core:admin`.
