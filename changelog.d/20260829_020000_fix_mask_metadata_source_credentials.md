### Fixed

#### `GET /api/v1/config` no longer returns metadata-source credentials in cleartext

The config endpoint masked the scalar `google_books_api_key` as `AIz****35bE`
while returning **the same key in full cleartext** a few fields later at
`metadata_sources[].credentials.apiKey` — two different maskings of one secret
in a single response. `MaskSecrets` covered the five scalar secret fields and
never walked `MetadataSources`, so any credential stored there (the Hardcover
token, and any provider added later) leaked the same way. The endpoint requires
`settings:manage`, which limits exposure, but the key was still readable by
anyone who could reach it and was being written into logs and transcripts by
anything that dumped the config.

`MaskSecrets` now masks every metadata-source credential value.

The deep copy in `maskMetadataSourceCredentials` is load-bearing, not defensive
style. `MaskSecrets` starts with `masked := cfg`, a **shallow** struct copy: the
`MetadataSources` slice header is copied but the backing array is shared with
the live `AppConfig`, and each `Credentials` map is a reference. Masking in
place would have overwritten the running process's real credentials with the
mask, and every provider client would then have authenticated with
`AIz****35bE`. The slice is reallocated and each map rebuilt so the response is
masked and the live config is untouched.

Empty credential values stay empty rather than becoming `****`, so the UI can
still distinguish "not configured" from "configured but hidden".

**Scope:** this covers the metadata-source credentials and the five scalar
secret fields. Several other secrets are still returned in cleartext — the OAuth
client secrets, the Deluge web password, and the download-client passwords and
Sabnzbd API key. Masking those safely needs the same round-trip work described
below done for each one, so they are tracked separately rather than half-fixed
here.

#### Saving settings no longer overwrites a stored API key with its own mask

Masking a value that the UI sends back is only safe if the server refuses the
echo, and it did not. Two paths were affected, and the second was already live
before this release:

- **Metadata-source credentials.** `metadata_sources` is not in `secretFieldKeys`,
  so it flows through `json.Unmarshal`, which replaces the whole slice —
  credentials included. The Settings page sends `metadata_sources` on *every*
  save, copying the credentials straight out of app state, which was populated
  from the masked `GET`. Without a guard, masking the response would have turned
  a disclosure bug into permanent credential loss on the very next save.
- **The five scalar secrets.** `secretFieldKeys` keeps these out of the JSON
  round-trip, but the explicit apply above it wrote whatever the payload carried,
  including an echoed mask. The browser avoids this by never seeding the input
  from the response, but that is a client-side convention, not an invariant —
  any script doing `GET` → edit → `PUT` destroyed the key. This was a
  pre-existing bug, not one introduced here.

Both are now rejected server-side: a value equal to the mask of the currently
stored secret is never written through. A genuinely new value still replaces the
old one, so rotation works. Clearing still works too — an empty scalar clears
the secret as before. An empty metadata-source credential is instead treated as
"unchanged", because that payload shape cannot distinguish "clear this" from
"absent", and refusing to destroy a credential through an ambiguous path is the
safer reading; clear one by supplying a new value or removing the source.

This failure would have been close to invisible. `MaskSecret` is idempotent —
`MaskSecret("AIz****35bE")` is `"AIz****35bE"` — so the response after a
destructive write is byte-identical to the response after a successful one. The
Settings page would keep displaying `AIz****35bE` over a wiped key, and nothing
would surface until the provider started returning 401.

Every property above is mutation-tested, including the call sites: deleting the
restore call from `UpdateConfig` (while leaving the function perfectly correct)
fails the round-trip test, and neutering the scalar guard fails the scalar test.

**Operators should rotate any API key that was previously exposed this way.**
