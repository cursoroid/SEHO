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
  library, streamed through Spotify's own Soloist client (or librespot).
- Lossless-preserving Spotify capture, with a quality selector: 16-, 24- or
  32-bit end to end, so the resolution of Spotify's lossless tier survives.
- A parametric equalizer with sound profiles, applied to local files and
  Spotify alike. Imports AutoEq and Equalizer APO files.
- A transport that reports the audio rather than the intent: a live level meter
  fed by the actual filter output, and `buffering` / `stalled` / `no sound`
  when playback claims to be running but nothing is coming out.
- A settings page: music directory, Redis address, Spotify credentials,
  bitrate. Stored in `~/.config/seho/config.json`.

## Requirements

- [Go](https://golang.org/dl/) 1.23+
- [Redis](https://redis.io/download) reachable at `REDIS_ADDR`
- [`mpv`](https://mpv.io/) for playback, controlled over its JSON IPC socket
- Docker, optional - only for Spotify, which streams through Spotify's Soloist
  client (Linux-only, so it runs in a container; see [docker/](docker/)).
  Spotify playback requires a Premium account.
- [`librespot`](https://github.com/librespot-org/librespot) 0.8+, optional -
  an alternative Spotify client (`brew install librespot`), kept as a fallback.
  Note it cannot stream on newer Spotify accounts; see below.
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
| `SEHO_SPOTIFY_BACKEND` | `spotify_backend` | `soloist` | `soloist` or `librespot` |
| - | `capture_bits` | `32` | Soloist capture depth: 16, 24 or 32 |
| - | `soloist_image` | `seho-soloist:latest` | container image to run |
| `SOLOIST_API_KEY` | - | - | Soloist key; otherwise read from the keychain |
| `SEHO_NO_METER` | - | - | Set to disable the level meter filter |
| `SEHO_MPV_LOG` | - | - | Set to a path to capture mpv's own verbose log |

The Spotify refresh token and the Soloist API key are not stored in that file:
they go to the macOS login keychain, falling back to `0600` files in
`~/.config/seho/` where no keychain is available.

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

Two clients can stream Spotify. Soloist is the default.

| | Soloist | librespot |
|---|---|---|
| Whose client | Spotify's own | third-party |
| Streams on new accounts | yes | **no** - see below |
| Runs on macOS | in Docker (Linux-only binary) | natively |
| Control | local WebSocket, event-driven | Spotify Web API, polled |
| Auth | Premium + API key + one pairing | two OAuth logins |

### Setup

1. Create an app at [developer.spotify.com](https://developer.spotify.com/dashboard)
   and add `http://127.0.0.1:8898/callback` as a redirect URI. Only the client
   id is needed - SEHO uses PKCE and never asks for a client secret. This is
   what browsing (search, Liked Songs, playlists) uses.
2. Press `,`, paste the client id, Save, then **Connect Spotify**.
3. For playback, build and pair the Soloist container once:
   see [docker/README.md](docker/README.md). Paste its API key on the settings
   page.

SEHO then starts the container on the first Spotify track you play, and stops it
on exit. Nothing is spawned if you only ever play local files.

Spotify audio is piped from the client into a second `mpv` instance rather than
sent straight to the system output, which is what lets the equalizer apply to
it. Browsing works without Premium; playback does not.

### librespot cannot stream on newer accounts

Since librespot 0.7, Spotify withholds audio decryption keys from newer Spotify
accounts, whatever login method librespot uses
([librespot#1649](https://github.com/librespot-org/librespot/issues/1649), open,
many reports). Everything up to the audio works - it signs in, registers as a
Connect device, Spotify accepts the play command - and then every track skips
with `error audio key 0 1`. Older accounts are unaffected and no client-side
workaround exists; contributors have tried OAuth, their own client ids and the
Android key. Also verified here: librespot 0.8's `--access-token` cannot reuse
SEHO's own token, because Spotify rejects a third-party client's token for a
librespot session (`Login request was denied: INVALID_CREDENTIALS`).

SEHO detects the key refusal and says so in the status line rather than leaving
the transport creeping along in silence. This is why Soloist is the default.

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
- **Pairing from the app**: Soloist pairing is a documented manual step; SEHO
  could run it (and the mDNS proxy) from the settings page.
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
