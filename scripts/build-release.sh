#!/bin/sh

set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
    echo "usage: $0 <version> [output-directory]" >&2
    exit 2
fi

version=${1#v}
output_dir=${2:-dist}

if ! printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?(\+[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$'; then
    echo "version must use semantic version syntax, for example v1.2.3 or v1.2.3-rc.1" >&2
    exit 2
fi

build_root=$(mktemp -d)
trap 'rm -rf "$build_root"' EXIT HUP INT TERM

mkdir -p "$output_dir"
output_dir=$(cd "$output_dir" && pwd)

build_target() {
    goos=$1
    goarch=$2
    extension=$3
    name="ps3mgr_${version}_${goos}_${goarch}"
    stage="$build_root/$name"

    mkdir -p "$stage"
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
        -buildvcs=false \
        -trimpath \
        -ldflags="-s -w -X ps3mgr/internal/cli.version=$version" \
        -o "$stage/ps3mgr$extension" \
        ./cmd/ps3mgr
    cp README.md "$stage/README.md"

    tar -C "$build_root" -czf "$output_dir/$name.tar.gz" "$name"
}

build_target linux amd64 ''
build_target linux arm64 ''
build_target darwin amd64 ''
build_target darwin arm64 ''
build_target windows amd64 .exe
build_target windows arm64 .exe

(cd "$output_dir" && sha256sum ./ps3mgr_"$version"_*.tar.gz > checksums.txt)
