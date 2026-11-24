### Fixed

- Under core identity, removing a guest's email from the Cloudflare guest policy now keeps the entry when the address is on the Cloudflare Access allow-list (`modules.books.cfaccess.allowed_emails`), the list that actually admits people, instead of the OIDC provider projection of `oauth_allowed_emails`. If the list cannot be read the entry is kept. Without core identity the check is unchanged.
