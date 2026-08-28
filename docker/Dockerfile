# Spotify Soloist runs on Linux only, so on macOS it runs here and hands its
# audio back to the host as raw PCM.
#
# Soloist can only emit to PipeWire or PulseAudio - there is no pipe or file
# backend - so a null sink gives us something to capture: parec reads the sink's
# monitor and writes PCM on stdout, which SEHO feeds to mpv for the equalizer.
FROM debian:stable-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      pulseaudio pulseaudio-utils ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

# The Soloist build is fetched at image build time rather than vendored: it is
# a 12MB proprietary binary and Spotify ships it per architecture.
ARG SOLOIST_ARCH=arm64
RUN curl -fsSL -o /tmp/soloist.tar.gz \
      "https://soloist-builds.spotifycdn.com/soloist_release_${SOLOIST_ARCH}.tar.gz" \
    && tar -xzf /tmp/soloist.tar.gz -C /tmp \
    && install -m 755 /tmp/soloist /usr/local/bin/soloist \
    && rm -f /tmp/soloist /tmp/soloist.tar.gz

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
