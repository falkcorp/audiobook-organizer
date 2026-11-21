### Changed

- The top bar and the page layout are now split into audiobook-free frames (`TopBarFrame`, `MainLayoutFrame`) and the audiobook pieces bound into them: the library search, the live-connection feed, the review banner and the global tag-write watcher. The top bar and layout look and behave the same: the same controls in the same order, the same search, the same "Connection lost / restored" chip, the same review banner above every page. This is groundwork for moving the layout into the shared shell package.
