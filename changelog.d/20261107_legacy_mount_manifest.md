### Changed

- Paths of the form `/api/<name>/v<N>/…` are reserved for versioned APIs and are no longer redirected to `/api/v1/`.
- Internal: core's router can mount the existing route engine as a legacy mount, with a reviewed manifest that names the permission of every one of its 571 routes (496 with the Audiobookshelf API off). Nothing serves through it yet.
