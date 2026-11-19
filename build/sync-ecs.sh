#!/usr/bin/env sh
# Copies the blink Go binaries from a symplify/easy-coding-standard release into bin/
# Usage: build/sync-ecs.sh [version], defaults to the latest release
set -e

VERSION="${1:-$(gh release view -R ecsphp/ecs --json tagName --jq .tagName)}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

curl -sSfL -o "$TMP/ecs.tar.gz" "https://github.com/ecsphp/ecs/archive/refs/tags/$VERSION.tar.gz"
tar -xzf "$TMP/ecs.tar.gz" -C "$TMP" --strip-components=1

for platform in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64; do
    cp "$TMP/bin/blink-$platform" "$ROOT/bin/blink-$platform"
    chmod +x "$ROOT/bin/blink-$platform"
done

echo "Synced blink binaries from ECS $VERSION"
