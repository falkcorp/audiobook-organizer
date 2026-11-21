### Added

- core/kernel: `mountIdentityRoutes`, the mount of the seven HTTP packages F4-05 left to the kernel (authhttp with its OIDC pair, rbachttp, audithttp, confighttp, storagehttp, modulehttp, uihttp). It takes the shared inputs once (one audit store, one client resolver, one owner-required reader) and refuses any input whose absence would open a route, drop an audit record or leave a route unavailable. Nothing calls it yet: the routes are mounted once stage 13 is in the chain (F4-08 A12).
