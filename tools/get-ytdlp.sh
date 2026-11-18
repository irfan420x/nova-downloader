#!/bin/sh
# get-ytdlp.sh — download the official yt-dlp release binary next to this
# script's repo (or to $1). The app also does this itself from Settings
# (one click); this is the terminal equivalent.
set -eu
OUT_DIR="${1:-$(dirname "$0")/..}"
mkdir -p "$OUT_DIR"
OS="$(uname -s)"
case "$OS" in
  Darwin) ASSET="yt-dlp_macos" ;;
  MINGW*|MSYS*|CYGWIN*) ASSET="yt-dlp.exe" ;;
  *) ASSET="yt-dlp" ;;
esac
echo "Downloading yt-dlp ($ASSET) to $OUT_DIR ..."
curl -L --fail -o "$OUT_DIR/yt-dlp" \
  "https://github.com/yt-dlp/yt-dlp/releases/latest/download/$ASSET"
chmod +x "$OUT_DIR/yt-dlp"
"$OUT_DIR/yt-dlp" --version
echo "Done. NOVA Downloader will find it next to the binary or on PATH."
