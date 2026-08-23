### Added

#### Collapsed sidebar now exposes Library's In Progress and Finished sub-nav

With the sidebar collapsed to icon-only width, the Library group rendered a
single icon with no way to reach the In Progress or Finished sub-routes — they
were reachable only after expanding the whole sidebar. Clicking the collapsed
Library icon now opens a menu listing All Books, In Progress, and Finished,
each navigating via the same `handleNavigation(path)` the expanded sidebar
uses. Selection highlighting in the menu reuses the existing
`isSubItemSelected()` decoded-search-param matcher (#2193), so the active
sub-item still highlights correctly and the original pathname-only comparison
bug is not reintroduced. The collapsed Library icon's broader "on some
Library-family page" selected state (matching `/library`, `/fingerprints`,
`/series`, `/authors`) is unchanged.
