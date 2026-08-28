#!/bin/sh
# Starts a PulseAudio null sink, Soloist, and a parec capture of the sink's
# monitor. Raw PCM goes to stdout; every log line goes to stderr, because one
# stray byte on stdout would corrupt the audio stream.
#
# Environment:
#   SOLOIST_API_KEY  developer API key (required)
#   SEHO_FORMAT      PCM sample format, s16le or s32le (default s32le)
#   SEHO_RATE        sample rate (default 44100)
#   SEHO_WS_PORT     WebSocket API port (default 5580)
#   SEHO_DEVICE_NAME Spotify Connect device name (default SEHO)
#   SEHO_PAIR        set to 1 to run pairing and exit
set -e

: "${SEHO_FORMAT:=s32le}"
: "${SEHO_RATE:=44100}"
: "${SEHO_WS_PORT:=5580}"
: "${SEHO_DEVICE_NAME:=SEHO}"

if [ -z "$SOLOIST_API_KEY" ]; then
  echo "SOLOIST_API_KEY is not set" >&2
  exit 64
fi

export XDG_RUNTIME_DIR=/tmp/pa
mkdir -p "$XDG_RUNTIME_DIR" /data/cache

# s32le carries more than 16 bits of resolution, which is the whole point of
# lossless playback: a 16-bit sink would quietly discard it before SEHO ever
# sees the audio.
pulseaudio -n --exit-idle-time=-1 \
  --load="module-native-protocol-unix" \
  --load="module-null-sink sink_name=seho rate=${SEHO_RATE} channels=2 format=${SEHO_FORMAT}" \
  --log-target=stderr --daemonize=yes 1>&2

# Pairing needs Spotify Connect discovery (mDNS), so it is a separate one-shot
# run rather than part of normal startup.
if [ "$SEHO_PAIR" = "1" ]; then
  exec soloist -n "$SEHO_DEVICE_NAME" -k "$SOLOIST_API_KEY" \
    -D /data -C /data/cache -w "0.0.0.0:${SEHO_WS_PORT}" --pair
fi

soloist -n "$SEHO_DEVICE_NAME" -k "$SOLOIST_API_KEY" \
  -D /data -C /data/cache -w "0.0.0.0:${SEHO_WS_PORT}" 1>&2 &

exec parec --device=seho.monitor \
  --format="$SEHO_FORMAT" --rate="$SEHO_RATE" --channels=2
