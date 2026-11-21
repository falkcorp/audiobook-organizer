### Changed

- The user menu is now split into an audiobook-free frame (`UserMenuFrame`, written against the shared shell's sign-in state and taking the profile-save and password-change calls as props) and a thin audiobook binding that supplies the existing sign-in context and API calls. The menu looks and behaves the same: the same account chip, Edit profile and Change password dialogs, validation and logout. This is groundwork for moving the layout into the shared shell package.
