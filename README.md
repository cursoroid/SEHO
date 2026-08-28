# SEHO

Self-hosted music library. A keyboard-driven terminal UI that indexes a music
directory into Redis and plays tracks through `mpv`.

## Features

- Scans a directory for `.mp3`, `.flac`, `.m4a` and `.ogg` files.
- Stores title, album, artist, year, track number and path in Redis.
- Browse the indexed library, fuzzy-search it, and play a track without
  leaving the terminal.
- A themed track table, live transport bar and embedded album art, with a
  responsive layout that adapts to the terminal width.

## Requirements

- [Go](https://golang.org/dl/) 1.23+
- [Redis](https://redis.io/download) reachable at `REDIS_ADDR`
- [`mpv`](https://mpv.io/) for playback, controlled over its JSON IPC socket
- `ffprobe` (ships with [FFmpeg](https://ffmpeg.org/)), optional - only used
  to read a track's duration at scan time; mpv backfills it during playback
  when `ffprobe` is unavailable or was skipped
- A truecolor terminal, so the Now Playing card's album art renders correctly

## Configuration

All optional:

| Variable | Default | Purpose |
|---|---|---|
| `MUSIC_DIR` | `~/Music` | Directory to index |
| `REDIS_ADDR` | `localhost:6379` | Redis address |

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
| `q` | Quit |

Logs go to `logs/seho.log`. Inspect the raw index with `redis-cli --scan --pattern 'music:*'`.

## Layout

The UI is responsive to terminal width:

| Width | Panes shown |
|---|---|
| ≥110 cols | sidebar + track table + Now Playing card (with album art) |
| 80–109 cols | sidebar + track table |
| <80 cols | track table only |

## To-Do

- **API**: expose the library over HTTP so clients other than the TUI can use it.
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
