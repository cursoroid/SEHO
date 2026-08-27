# SEHO

Self-hosted music library. A terminal UI that indexes a music directory into
Redis and plays tracks through `ffplay`.

## Features

- Scans a directory for `.mp3`, `.flac`, `.m4a` and `.ogg` files.
- Stores title, album, artist, year, track number and path in Redis.
- Browse the indexed library and play a track without leaving the terminal.

## Requirements

- [Go](https://golang.org/dl/) 1.23+
- [Redis](https://redis.io/download) reachable at `REDIS_ADDR`
- `ffplay` (ships with [FFmpeg](https://ffmpeg.org/)) for playback

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

| Key | Action |
|---|---|
| `enter` | Activate the selected menu entry or play the selected track |
| `esc` | Leave the library, back to the menu |
| `/` | Filter the current list |
| `ctrl+c` | Stop playback and quit |

Logs go to `logs/seho.log`. Inspect the raw index with `redis-cli --scan --pattern 'music:*'`.

## To-Do

- **API**: expose the library over HTTP so clients other than the TUI can use it.
- **Streaming**: serve audio over HTTP instead of shelling out to a local `ffplay`.
- **Frontend**: a web UI, once the API exists.
- **Docker**: worth doing once there is a server to run; a TTY-only TUI is not.
- **Search**: query the library by tag, artist or album.
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
