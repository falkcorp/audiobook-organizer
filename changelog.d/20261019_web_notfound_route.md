### Fixed

#### Signed-in web UI no longer shows a blank page for unknown URLs, and `/config/api-keys` lands on API Keys

The signed-in route table had no catch-all, so any unknown path rendered an empty
page inside the normal layout. Going to `/config/api-keys` (the Audiobookshelf web
client's API-keys URL, which ABS apps and docs link to) looked like broken API-key
generation. Unknown paths now show a Page not found screen that names the requested
path and links to the Dashboard and Settings. `/config/api-keys` redirects to
`/settings#api-keys`, which opens the API Keys tab, and `/config` redirects to
`/settings`, and the same paths under the `/audiobookshelf` router base are mapped too (`/audiobookshelf` goes to the Dashboard). Other Audiobookshelf paths such as `/config/users` have no equivalent
here and show the not-found page.
