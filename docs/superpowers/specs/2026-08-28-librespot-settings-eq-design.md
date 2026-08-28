# Spotify (librespot), settings page, and equalizer

Date: 2026-08-28
Status: approved, implementation authorised without a spec review round

## Goal

Three additions to SEHO, in one design because they share plumbing:

1. **Spotify as a second library source** — search, Liked Songs and playlists,
   browsable in the same table as the local library, audio via `librespot`.
2. **A settings page** — configuration moves out of environment variables only,
   into a persisted config file with a full-screen editor.
3. **An equalizer with sound profiles** — an mpv filter chain, driven by
   profiles derived from published MacBook Pro tunings, applied to *both*
   local and Spotify playback.

Non-goals: a web UI, an HTTP API, Docker, scrobbling, offline caching of
Spotify results, per-track EQ memory, room correction.

## Decisions taken during brainstorming

| Question | Decision |
|---|---|
| What librespot does here | Full Spotify browse + search, not just a Connect sink |
| Audio and control path | Spawned `librespot` binary (Homebrew, like `mpv`), Spotify Web API for browse/state |
| Settings storage | `~/.config/seho/config.json`, env vars still override; refresh token in the macOS Keychain |
| Settings UI | Full-screen page |
| Browse scope for v1 | Search + Liked Songs + playlists |
| EQ scope | Both sources — librespot's audio is piped through mpv |

## Architecture

### Playback: one interface, two backends

```go
type Backend interface {
	Load(target string) error   // file path, or spotify: URI
	TogglePause() error
	Seek(delta float64) error
	SetVolume(v int) error
	Events() <-chan Event
	Close() error
}
```

`*Player` (mpv, `player.go`) already satisfies this; the interface is a
declaration, not a rewrite. `*SpotifyBackend` (`spotify_player.go`) is the
second implementation. `UI.pl` becomes a `Backend`, and `playRow` selects the
backend from the item's source, pausing the other.

Two implementations exist, so one interface is justified. No registry, no
plugin loader.

### Audio routing

Because the EQ must cover Spotify too, librespot's audio is piped into mpv
rather than sent to the system output:

```
local file ──────────────────────────────► mpvLocal ──► af chain ──► output
spotify ──► librespot --backend pipe ──► fifo ──► mpvSpot ──► af chain ──► output
```

Two mpv instances, not one. A single instance would have to hold both a
seekable file and an endless raw stream, and would conflate their `end-file`
semantics; two processes cost one extra `StartPlayer` call and keep today's
local playback logic untouched.

- fifo: `$TMPDIR/seho-spotify-$PID.pcm`, `mkfifo`, removed on close.
- `librespot --backend pipe --device <fifo>` emits S16LE 44100 Hz stereo.
- `mpvSpot` loads the fifo with `--demuxer=rawaudio`,
  `--demuxer-rawaudio-format=s16le`, `--demuxer-rawaudio-rate=44100`,
  `--demuxer-rawaudio-channels=2`, plus `--cache=no` and a small
  `--audio-buffer` so the transport does not lag behind reality.
- Volume for both sources is mpv's, so Web API volume calls are not used.

Two known traps, both handled deliberately:

1. librespot's pipe backend closes the fifo between tracks, which mpv reads as
   EOF. SEHO holds its own write handle on the fifo open for the process
   lifetime so a reader never sees EOF, **and** reloads the fifo on `end-file`
   while Spotify is the active source. Belt and braces: the dummy writer is the
   fix, the reload is the recovery if it ever fails.
2. Raw pipe playback has no duration and a meaningless `time-pos`. In Spotify
   mode the transport clock comes from the Web API poller instead (below), and
   mpv's clock events are ignored.

This is the least certain part of the design. Implementation starts with a
throwaway spike that proves `librespot --backend pipe` → fifo → mpv produces
sound before any UI work depends on it.

### Transport events for Spotify

mpv pushes `time-pos` at roughly 16 Hz. The Web API answers
`GET /me/player` at about 1 Hz before rate limits matter. The Spotify backend
therefore polls once a second and **interpolates** the position from a
monotonic clock between polls, emitting the same `Event` values the UI already
consumes. Without interpolation the progress bar's shimmer and comet head
(`progressBar` in `ui.go`) step once per second instead of gliding.

A poll that contradicts the interpolation wins — the API is the authority, the
interpolation only fills gaps.

### Library model

| | Local | Spotify |
|---|---|---|
| Storage | Redis index, unchanged | fetched live, never indexed |
| Identity | `item.path` = file path | `item.path` = `spotify:track:…` |
| Source tag | `item.src == srcLocal` | `item.src == srcSpotify` |
| Search | fuzzy over `u.all`, unchanged | remote query, submitted on <kbd>enter</kbd> |

`item` gains one field, `src`. Spotify results are never written to Redis:
they are a view, not a library, and a stale mirror is worse than a request.

Sidebar becomes a flat list with a dim separator row:

```
All Tracks · Artists · Albums · Tags · Recent
──────────
Spotify Search · Liked Songs · Playlists
```

Playlists drill down through the existing group-row mechanism
(`filterByGroup`), so no new navigation concept is introduced.

### Files

| File | Contents |
|---|---|
| `config.go` | `Config` struct, JSON load/save, env overrides, keychain get/set, `hw.model` detection |
| `spotify_api.go` | PKCE OAuth, token refresh, search / liked / playlists / player endpoints |
| `spotify_player.go` | librespot spawn, fifo, poller, `Backend` implementation |
| `eq.go` | `band`/`profile` types, baked-in profile table, AutoEq + Equalizer APO parser, mpv `af` chain builder |
| `settings.go` | full-screen settings page and full-screen sound page |
| `player.go` | `Backend` interface extraction, rawaudio start options |
| `ui.go` | source routing, sidebar entries, remote search, page switching |

## Authentication

Spotify no longer accepts password login, so both halves authenticate
separately. This is a cost of the design, not an oversight.

| Half | Flow | Cached |
|---|---|---|
| SEHO → Web API | Authorization Code with PKCE, user's own client id, loopback redirect `http://127.0.0.1:8898/callback`, no client secret | refresh token in Keychain |
| librespot → Spotify session | librespot's own OAuth (`--enable-oauth` in 0.8), URL captured from its output | librespot's `--cache` directory, silent afterwards |

The settings page runs both under a single "Connect Spotify" action: SEHO's
browser tab first, then librespot's. Scopes requested:
`user-read-playback-state`, `user-modify-playback-state`, `user-library-read`,
`playlist-read-private`.

The client id is not a secret and lives in the plain config file. The refresh
token goes to the Keychain:

```
security add-generic-password -s seho -a spotify-refresh -w <token> -U
security find-generic-password -s seho -a spotify-refresh -w
```

On any platform without `security`, or when the command fails, the token falls
back to `~/.config/seho/token.json` at mode `0600`. One function per direction
with a `runtime.GOOS` switch — no interface for two cases.

Exact librespot 0.8 flag names and OAuth output format are verified against the
installed binary before code depends on them.

## Configuration

`~/.config/seho/config.json`:

```json
{
  "music_dir": "~/Music",
  "redis_addr": "127.0.0.1:6379",
  "spotify_client_id": "",
  "device_name": "SEHO",
  "bitrate": 320,
  "volume": 100,
  "eq": { "enabled": true, "profile": "mbp14", "bands": [] }
}
```

Precedence: environment variable → config file → built-in default. An
env-overridden field renders read-only on the settings page with a dim
`set by MUSIC_DIR` note; offering to edit a value the environment will stomp
would be a lie.

`eq.bands` is empty unless the user has edited a profile's curve by hand, in
which case it holds the modified bands and `profile` records what it started
from.

## Equalizer

One representation for every profile, whatever its origin:

```go
type band struct{ freq, gain, q float64 } // peaking

type profile struct {
	name   string
	preamp float64
	bands  []band
	extra  []string // optional extra af entries, e.g. "dynaudnorm" for Night
}
```

Rendered to an mpv filter chain — `volume` for the preamp, chained `equalizer`
biquads for the bands, `extra` appended — and pushed to both mpv instances over
the IPC socket that already exists. Applied live as a band moves.

`e` opens a full-screen sound page (same `tview.Pages` mechanism as settings):
profile list on the left, curve on the right, drawn with the eighth-block
glyphs already in `theme.go`.

```
 +12 ┤        ▂▄
   0 ┤▄▄▆█▆▄▄▄▄▄
 -12 ┤
     └──────────
      31 62 125 250 500 1k 2k 4k 8k 16k
```

### Profile sources

| Repo | What it provides | How it is used |
|---|---|---|
| [AutoEq](https://github.com/jaakkopasanen/AutoEq) | thousands of headphone `ParametricEQ.txt` files: a preamp line plus peaking filters | maps directly onto `band`; one parser also accepts Equalizer APO configs |
| [asahi-audio](https://github.com/AsahiLinux/asahi-audio) | per-Mac speaker tuning; `j314` / `j316` are the 14" and 16" MacBook Pro | EQ stages are converted to `band` values; its impulse responses remain available through mpv's `afir` if a curve proves insufficient |
| [mbp-16-bootcamp-speaker-mod](https://github.com/Naozumi520/mbp-16-bootcamp-speaker-mod) | MacBook Pro 16" 2019 tuning | reference curve only — Windows VST plugins, not portable |
| [OnlyEQ](https://github.com/zollans/OnlyEQ), [open-mac-eq](https://github.com/sean-o-sullivan/open-mac-eq) | macOS system-wide equalizers | not profile sources; they confirm which import formats are worth supporting |

What does **not** port from asahi-audio: its `bankstown` bass enhancement and
`triforce` multiband compressor are LADSPA plugins with no ffmpeg equivalent.
The speaker profiles here are the EQ half of that work, and the profile
description says so rather than implying parity.

Baked-in profiles: Flat, MacBook Pro 14" speakers, MacBook Pro 16" speakers,
MacBook Air / 13" speakers, Generic laptop (60 Hz high-pass so the cones stop
flapping, plus a presence lift), Night (`dynaudnorm`), Vocal, Bass. Plus "load
from file" for any AutoEq or Equalizer APO text file on disk.

No profile numbers are invented. Each baked-in curve is derived at
implementation time from the upstream file it cites, and `eq.go` records the
source and conversion in a comment beside the data. A curve that cannot be
sourced is not shipped.

`sysctl hw.model` at startup selects the matching speaker profile on first run.
This machine reports `Mac16,1` (MacBook Pro 14", M4). asahi-audio covers M1 and
M2 hardware only, so M3/M4 models map to the nearest same-chassis profile and
the page labels it as approximate.

## Error handling

| Failure | Behaviour |
|---|---|
| `librespot` not on PATH | Spotify sidebar rows dim, refuse enter with a reason; settings page shows the `brew install librespot` hint |
| No client id configured | same, with "connect Spotify in settings" |
| Web API 401 | refresh once, retry once, then surface "Spotify session expired" |
| Web API 403 (no Premium) | "Spotify Premium required for playback"; browse continues to work |
| Web API 429 | back off for the `Retry-After` interval, keep the last known state |
| Network unreachable | status line error; the local library keeps working entirely |
| Keychain unavailable | fall back to the `0600` token file, note it on the settings page |
| Redis unreachable at save time | settings page keeps the old connection and reports the failure inline |
| fifo/mpv desync | reload the fifo on `end-file`; three failures in a row disables the Spotify source with a message |

## Testing

Tests stay local and untracked, following this repo's existing habit
(`*_test.go` are present but not committed).

- `config.go`: precedence order, tilde expansion, round-trip save/load, keychain fallback path.
- `spotify_api.go`: PKCE verifier and challenge shape, `httptest` servers for search / liked / playlists parsing, 401-refresh-retry, 429 back-off.
- `spotify_player.go`: poller interpolation against a fake clock, poll-wins-over-interpolation, `end-file` reload counter.
- `eq.go`: AutoEq and APO parsing including malformed lines, `af` chain string for a known profile, gain clamping, `hw.model` mapping.
- `ui.go`: source routing in `playRow`, sidebar separator row is inert, remote search does not clobber the local base view.

Manual verification: the spike above, then a real track from each source with
the EQ visibly moving.

## Verified against the live API and librespot, 2026-08-28

What the implementation found, recorded here because the design guessed
differently:

| Assumption in this design | What is actually true |
|---|---|
| The pipe target needs proving | `--device <fifo>` works. The log line `Using StdoutSink (pipe)` is printed unconditionally; the file is opened lazily in the sink's `start()`, so it says nothing about the target |
| librespot's second login might be avoidable via `--access-token` | Rejected by Spotify: `could not initialize spirc: Login request was denied: INVALID_CREDENTIALS`. A third-party client's token cannot open a librespot session. Two logins is what Spotify permits |
| "Authenticated as" means librespot is usable | It does not. With a stale credential the session authenticates and is then refused at spirc, so the device never registers. Device visibility in the Web API is the only trustworthy readiness test |
| `/playlists/{id}/tracks` lists a playlist | Returns 403. The endpoint is `/playlists/{id}/items`, and each entry keys on `item`, not `track`. Playlist objects also carry the count under `items.total`, not `tracks.total` |
| Premium is the only playback prerequisite | It is not. Spotify withholds audio keys from newer accounts on every librespot login path (librespot#1649). Everything up to decryption works; every track then skips. No client-side fix exists |
| `Load` can be called inline from the UI | It cannot. Waiting for a librespot session can take as long as a human in a browser, so the Spotify play path runs off the tview goroutine |
| Soloist exposes a stream-quality setting | It does not. `soloist --help` (1.3.7) has no bitrate or quality option, so the depth SEHO captures at is the only quality knob it has: `s16le` truncates a lossless stream, `s24le` matches Spotify's 24-bit maximum exactly, `s32le` adds headroom. All three verified working through the container's null sink and `parec`, and through mpv's rawaudio demuxer |
| A FIFO can carry the capture into mpv | Not on macOS. POSIX leaves opening a FIFO `O_RDWR` undefined, and macOS turns that single handle into a private channel, so writes never reach mpv - which looked idle while bytes flowed and the filter chain was applied. `os.Pipe` plus `cmd.ExtraFiles` and `fd://3` is the working path |
| `ctl trace` emits bare JSON lines | Each line is prefixed with a millisecond timestamp, so a `HasPrefix("{")` filter drops every event. The parser locates the object within the line, and its tests are built from payloads captured off a live daemon |
| mpv can be pointed at the pipe as soon as it exists | It parks: the rawaudio demuxer never starts on a stream with no bytes yet. `Load` is deferred until the first captured byte arrives |
| Soloist reports `duration_ms` under `item.playback` | It reports it under `item.decorations.playback`. SEHO read the wrong path, so it never learned a track's length - and its end-of-track test (position past duration) could never fire. A Spotify track therefore had no end and the transport showed it playing forever |
| Soloist sends position updates while playing | It sends one anchor per track and nothing more. Interpolation is not a nicety, it is the only clock - so the interpolated position is also the only thing that can notice a track running out |
| A finished Spotify track means Soloist stops | It does not. Soloist follows the Spotify context and advances to its own next track, so SEHO's track ends while the status stays "playing". A reported URI other than the one SEHO asked for is the signal |
| Every event can be dropped when the consumer is behind | Position updates can; state changes cannot. `end-file` was droppable, so a UI busy for a few seconds (the settings page ran docker calls on the tview goroutine) could miss the one event that says a track finished |
| A Player's event channel can be left unread | Not once state events block: the channel is fed by the IPC reader, so an unread one stalls every later command and the backend looks dead while the audio is fine. The streaming backends now drain their inner mpv, forwarding what it knows better than the source does (core-idle, paused-for-cache) |
| mpv reporting "playing" means audio is audible | It does not. With a stalled audio device (Bluetooth headphones that went away, a wedged CoreAudio) mpv sits at `audio=playing` with a frozen clock forever. Whether sound is coming out has to be measured: `astats` metadata through `af-metadata/<label>` gives the level, and a clock that stops moving gives the stall |

## Implementation order

1. Spike: librespot pipe → fifo → mpv makes sound. Throwaway.
2. `config.go` + keychain, wired into `main.go` with env precedence intact.
3. Settings page, editing what already exists (music dir, redis addr) — usable before any Spotify code lands.
4. `eq.go` + sound page against `mpvLocal` only.
5. `Backend` interface extraction.
6. `spotify_api.go` with OAuth and browse endpoints.
7. `spotify_player.go`, fifo, poller, second mpv instance.
8. `ui.go` source routing, sidebar entries, remote search.
9. EQ applied to both instances; README updated.

Each step leaves the app runnable.
