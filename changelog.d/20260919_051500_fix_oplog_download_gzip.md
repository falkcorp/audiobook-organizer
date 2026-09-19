### Fixed

- "Download full log (.gz)" on the operation activity panel now saves a file that opens. The handler streams its own gzip body, and the router-wide gzip middleware (gin-contrib/gzip v1.2.7) wrapped it again: it passed the first chunk through raw, stripped `Content-Encoding`, and compressed every later chunk, so every browser download failed `gunzip` with "data stream error". The download route is now excluded from the middleware, and the middleware's construction is shared with its tests (`compressionMiddleware`) so the exclusion list can't drift from production.
