### Changed

- Web tests that replaced `services/api` (and the `services/*Api` splits) with a hand-written object now spread the real module and override only what they drive (`importOriginal`). 55 files / 68 mocks converted, no assertion changes. A hand-written full mock broke Library (#4910) and 28 App tests (#4920) when an export was added; a new guard test (`web/src/test/fullServiceMockGuard.test.ts`) fails on any test that mocks one of those modules with a factory that does not keep the real module.
