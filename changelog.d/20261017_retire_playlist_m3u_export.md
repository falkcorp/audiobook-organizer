The playlist M3U export route, `GET /api/v1/playlists/:id/export.m3u`, is retired.
Nothing in the web app, scripts or the AudioBooth client called it. It now answers
410 Gone with a message saying it has no replacement, for one release, and its
handler and tests are deleted. The collection routes and every reading and
position route are unchanged, and no listening data is touched.
