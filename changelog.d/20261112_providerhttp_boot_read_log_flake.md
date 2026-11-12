### Fixed

- core/integrations/providerhttp: the log line that reports a boot read recovering ("read after an earlier failure") is now written before the persisted holds and scope mappings become visible, not after. A caller that saw a recovered hold could previously read the log before the line was written, which made `TestHolds_BootReadRetriedLazily` fail intermittently.
