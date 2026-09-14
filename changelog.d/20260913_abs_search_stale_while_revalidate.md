### Fixed

- ABS clients: a search, author list or filter menu opened right after a cache expired waited on a full-library rebuild (20-32s in production), long enough for the phone client to give up and show an empty result. The contributor index, `/filterdata` document and series grouping now keep serving the previous build while a new one is built in the background; a request only waits when no build exists yet, and all three are warmed at startup.
