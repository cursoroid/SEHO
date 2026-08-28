# Soloist container

[Spotify Soloist](https://developer.spotify.com/documentation/soloist) is
Spotify's own headless client. SEHO prefers it over librespot because it
actually streams: librespot is refused audio decryption keys outright on newer
Spotify accounts ([librespot#1649](https://github.com/librespot-org/librespot/issues/1649)).

Soloist is Linux-only and can emit audio only to PipeWire or PulseAudio - there
is no pipe or file backend - so on macOS it runs here, and the container hands
its audio back as raw PCM:

```
docker[ soloist -> pulse null sink -> parec ] -> stdout -> pipe -> mpv -> EQ -> output
```

That indirection is what keeps SEHO's equalizer in the path for Spotify as well
as for local files.

## Requirements

- Docker (Colima works; the image runs natively on Apple silicon)
- Spotify **Premium**
- A Soloist API key from the [developer dashboard](https://developer.spotify.com/dashboard)

## Build

```bash
docker build --platform linux/arm64 -t seho-soloist:latest ./docker
# on Intel: --platform linux/amd64 --build-arg SOLOIST_ARCH=x86_64
```

## Pair, once

Soloist authenticates by being picked in a Spotify app over Spotify Connect
**discovery (mDNS)**. It never registers server-side, so it cannot be paired
from the Web API, and a container on Docker's NAT is invisible to the LAN.

On macOS the way through is to advertise the container yourself, with the
Spotify **desktop app on the same Mac** as the client.

1. Give the Docker VM an address the host can reach. With Colima:

   ```bash
   colima stop && colima start --network-address
   ```

2. Start Soloist in pairing mode on the VM's network:

   ```bash
   docker run --rm --name seho-pair --network host \
     -v seho-soloist-data:/data \
     -e SOLOIST_API_KEY="$SOLOIST_API_KEY" -e SEHO_PAIR=1 \
     seho-soloist:latest
   ```

   It logs `pairing mode - connect to "SEHO" from your Spotify app`.

3. Find the VM address and the zeroconf port. The port is the TCP listener that
   is neither SSH nor the WebSocket API (5580):

   ```bash
   colima ssh -- ip -4 addr show col0 | grep inet     # e.g. 192.168.65.2
   colima ssh -- ss -lnt                              # e.g. 54911
   curl "http://192.168.65.2:54911/zc?action=getInfo"  # should answer remoteName SEHO
   ```

4. Advertise it from the host, so the desktop app can discover what multicast
   cannot reach. `CPath` must match the endpoint above:

   ```bash
   dns-sd -P SEHO _spotify-connect._tcp local 54911 \
     seho-soloist.local 192.168.65.2 CPath=/zc VERSION=1.0 Stack=SP
   ```

   Leave it running.

5. Open Spotify on the Mac and pick **SEHO** in the device picker. The pairing
   container logs `pairing settled` and exits. Stop the `dns-sd` proxy.

Credentials are stored in the `seho-soloist-data` volume, so this is a one-time
step per machine. Afterwards SEHO starts the container itself with only the
WebSocket port published on loopback - no host networking, no mDNS.

## Lossless

Spotify's lossless tier carries more than 16 bits per sample. The capture format
therefore matters: at `s16le` the extra resolution is discarded inside the
container, before SEHO ever sees it. With `Lossless capture` enabled (the
default) the null sink, the capture and mpv all run at 32 bits.

Soloist does not report which tier it is streaming - there is no quality field
anywhere in its WebSocket payloads - so SEHO does not display one. What it can
guarantee is that it does not degrade the stream it is given.

## Environment

| Variable | Default | Purpose |
|---|---|---|
| `SOLOIST_API_KEY` | - | developer API key (required) |
| `SEHO_FORMAT` | `s32le` | PCM sample format, `s16le` or `s32le` |
| `SEHO_RATE` | `44100` | sample rate |
| `SEHO_WS_PORT` | `5580` | Soloist WebSocket API port |
| `SEHO_DEVICE_NAME` | `SEHO` | Spotify Connect device name |
| `SEHO_PAIR` | - | `1` to pair and exit |
