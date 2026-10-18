### Fixed

- `GET /api/v1/itunes/pid-integrity` wrote an error response and then carried on and wrote a second JSON body after it, so the response held two documents. The iTunes library ITL path is optional for this census: with none configured it now answers one 200 with the census (`tracks_in_itl` is 0), and a path that is set but invalid answers one 500 and stops.
