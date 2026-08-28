# SEHO

Self-hosted music library. A keyboard-driven terminal UI that indexes a music
directory into Redis and plays tracks through `mpv`, with Spotify browsing and
playback through `librespot` and an equalizer that covers both.

## Features

- Scans a directory for `.mp3`, `.flac`, `.m4a` and `.ogg` files.
- Stores title, album, artist, year, track number and path in Redis.
- Browse the indexed library, fuzzy-search it, and play a track without
  leaving the terminal.
- A themed track table, live transport bar and embedded album art, with a
  responsive layout that adapts to the terminal width.
- Spotify search, Liked Songs and playlists in the same table as the local
  library, played through `librespot`.
- A parametric equalizer with sound profiles, applied to local files and
  Spotify alike. Imports AutoEq and Equalizer APO files.
- A settings page: music directory, Redis address, Spotify credentials,
  bitrate. Stored in `~/.config/seho/config.json`.

## Requirements

- [Go](https://golang.org/dl/) 1.23+
- [Redis](https://redis.io/download) reachable at `REDIS_ADDR`
- [`mpv`](https://mpv.io/) for playback, controlled over its JSON IPC socket
- [`librespot`](https://github.com/librespot-org/librespot) 0.8+, optional -
  only needed for Spotify (`brew install librespot`). Spotify playback also
  requires a Premium account.
- `ffprobe` (ships with [FFmpeg](https://ffmpeg.org/)), optional - only used
  to read a track's duration at scan time; mpv backfills it during playback
  when `ffprobe` is unavailable or was skipped
- A truecolor terminal, so the Now Playing card's album art renders correctly

## Configuration

Settings live in `~/.config/seho/config.json` and are edited in the app with
`,`. Environment variables still win over the file, so the older workflow keeps
working; a field the environment controls shows read-only on the settings page.

| Variable | Config key | Default | Purpose |
|---|---|---|---|
| `MUSIC_DIR` | `music_dir` | `~/Music` | Directory to index |
| `REDIS_ADDR` | `redis_addr` | `127.0.0.1:6379` | Redis address |
| `SPOTIFY_CLIENT_ID` | `spotify_client_id` | - | Spotify app client id |
| `SEHO_DEVICE_NAME` | `device_name` | `SEHO` | Name librespot advertises |
| `SEHO_BITRATE` | `bitrate` | `320` | Spotify bitrate: 96, 160 or 320 |

The Spotify refresh token is not stored in that file: it goes to the macOS
login keychain, falling back to `~/.config/seho/token.json` at mode `0600`
where no keychain is available.

## Run

```bash
go run .
```

Keeping the variables in a `.env` file works too — the shell loads it, not the app:

```bash
set -a; . ./.env; set +a
go run .
```

`.env` is gitignored. Never commit it.

## Usage

Keyboard-only; there is no mouse support.

| Key | Action |
|---|---|
| `↑ / ↓` | Move the selection in the focused pane |
| `enter` | Play the selected track, or activate the selected sidebar filter |
| `/` | Open fuzzy search |
| `esc` | Close search (also clears the current filter) |
| `space` | Pause / resume |
| `← / →` | Seek 5s back / forward |
| `n / p` | Next / previous track |
| `- / =` | Volume down / up |
| `tab` / `shift+tab` | Cycle focus between the table and the sidebar (only when the sidebar fits - see Layout below) |
| `s` | Rescan the music directory |
| `,` | Settings page |
| `e` | Sound page (equalizer and profiles) |
| `q` | Quit |

On the sound page: `↑↓` picks a profile or band, `←→` trims the selected band
by 0.5 dB, `0` zeroes it, `r` restores the profile as published, `s` saves.

Logs go to `logs/seho.log`. Inspect the raw index with `redis-cli --scan --pattern 'music:*'`.

## Layout

The UI is responsive to terminal width:

| Width | Panes shown |
|---|---|
| ≥110 cols | sidebar + track table + Now Playing card (with album art) |
| 80–109 cols | sidebar + track table |
| <80 cols | track table only |

## Spotify

1. `brew install librespot`.
2. Create an app at [developer.spotify.com](https://developer.spotify.com/dashboard)
   and add `http://127.0.0.1:8898/callback` as a redirect URI. Only the client
   id is needed - SEHO uses PKCE and never asks for a client secret.
3. Press `,`, paste the client id, Save, then **Connect Spotify**. Two browser
   trips happen on the first run: one for SEHO's own API access, one for
   librespot's audio session. Both are cached afterwards.

Spotify audio is piped from librespot into a second `mpv` instance rather than
sent straight to the system output, which is what lets the equalizer apply to
it. Browsing works without Premium; playback does not.

## Sound profiles

Profiles are parametric: a preamp plus peaking, shelf and pass filters,
rendered into an `mpv` filter chain. Sources are cited in `eq.go` beside each
curve.

| Profile | Source |
|---|---|
| MacBook Pro speakers, MacBook Pro vocal clarity | the portable EQ stages of [mbp-16-bootcamp-speaker-mod](https://github.com/Naozumi520/mbp-16-bootcamp-speaker-mod) |
| AirPods Max, AirPods Pro 2 | [AutoEq](https://github.com/jaakkopasanen/AutoEq) measurements |
| Night, Bass boost | authored here |

Any AutoEq `ParametricEQ.txt` or Equalizer APO config can be imported, so the
whole AutoEq catalogue is usable.

[asahi-audio](https://github.com/AsahiLinux/asahi-audio) is deliberately not
used, despite being the best open-source MacBook speaker work: its per-Mac
configs are six-channel crossovers with per-driver convolution that *replace*
Apple's DSP on Linux. macOS applies that correction downstream of anything SEHO
sends it, so those curves would double-process rather than correct. The
speaker profiles here are taste, not correction, and the sound page says so.

## To-Do

- **API**: expose the library over HTTP so clients other than the TUI can use it.
- **Spotify albums and artists**: only search, Liked Songs and playlists so far.
- **Single Spotify login**: librespot 0.8 accepts `--access-token`, which could
  remove the second browser trip once its scope requirements are confirmed.
- **Streaming**: serve audio over HTTP instead of shelling out to a local `mpv` instance.
- **Frontend**: a web UI, once the API exists.
- **Docker**: worth doing once there is a server to run; a TTY-only TUI is not.
- **Durability**: enable Redis AOF or RDB, or the scraped tags vanish on restart.

## Contributing

Contributions are welcome. Open an Issue or a Pull Request.

## Contributors

<a href="https://github.com/cursoroid/SEHO/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=cursoroid/SEHO" />
</a>

## License

MIT. See [LICENSE](LICENSE).

## Contact

[prathameshmudgale@gmail.com](mailto:prathameshmudgale@gmail.com)
