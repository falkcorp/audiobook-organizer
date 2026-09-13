### Fixed

- Review workspace: clicking a cover on a metadata candidate card (the "Proposed" cover, and
  the "Current" one, which share the same viewer) now opens the shared full-screen cover
  viewer, bounded to 90% of the viewport and showing the image at natural resolution, instead
  of a small `maxWidth="sm"` dialog whose image was sized as a percentage of a shrink-wrapped
  box. Amazon/Audible covers open at their original resolution (the `._SL500_.`-style size
  directive is dropped for the enlarged view), and a cover that fails to load now says so
  instead of rendering as a few-pixel broken image. Closes on Escape, backdrop click, the
  close button, or a click on the image.
