### Changed

- The operations bell (the notification icon in the top bar) is now split into an audiobook-free frame (`OperationsIndicatorFrame`) and the audiobook pieces bound into it: the operations store, the operation labels, cancel, the activity panel, the parsed import details, and the Review and Undo buttons on finished runs. The bell looks and behaves the same: the same badge, sections, collapse state, ordering and buttons. This is groundwork for moving the layout into the shared shell package.
