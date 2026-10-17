### Removed

#### qBittorrent and SABnzbd download-client code and settings

Removed unused qBittorrent and SABnzbd client code and settings. The generic download-client package `internal/download` is deleted along with the `download_client.torrent.qbittorrent` and `download_client.usenet` settings, which nothing ever read. The Deluge settings under `download_client.torrent` are kept because three Deluge fallbacks still use them. An existing config file that still contains the removed keys keeps loading, and logs a removed-setting warning for each retired block. Saving the config scrubs the retired blocks (including any stored qBittorrent password or SABnzbd API key), and a config update that sets them is refused with a removal message.
