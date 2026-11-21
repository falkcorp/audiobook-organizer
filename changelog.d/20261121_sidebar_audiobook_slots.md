### Changed

- The sidebar is now split into an audiobook-free frame (`SidebarFrame`, which renders whatever entries it is handed) and the audiobook entries themselves (`web/src/modules/books/layout/nav.ts`), with the Review badge count read through a hook the frame calls per entry. The sidebar looks and behaves the same: the same entries in the same order, the same links, the same Library group and the same live Review count. This is groundwork for moving the layout into the shared shell package.
