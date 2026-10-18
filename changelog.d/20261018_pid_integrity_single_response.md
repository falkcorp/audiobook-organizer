### Fixed

- `GET /api/v1/itunes/pid-integrity` with no iTunes library ITL path configured wrote its 400 error and then carried on and wrote a second JSON body after it, so the response held two documents. It now answers one 400 and stops.
