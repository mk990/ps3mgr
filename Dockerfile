# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /src

COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build \
      -buildvcs=false \
      -trimpath \
      -ldflags="-s -w -X ps3mgr/internal/cli.version=${VERSION}" \
      -o /out/ps3mgr \
      ./cmd/ps3mgr

FROM python:3.13-slim-bookworm
RUN apt-get update \
    && apt-get install -y --no-install-recommends makefs \
    && rm -rf /var/lib/apt/lists/* \
    && pip install --no-cache-dir mkpfs==1.1.0
USER 65532:65532

ARG VERSION=dev
ARG COMMIT=unknown
ARG SOURCE=https://github.com/unknown/ps3mgr

LABEL org.opencontainers.image.title="PlayStation Manager" \
	  org.opencontainers.image.description="PS2 OPL/USB and PS4 FPKG conversion, PS3 FTP, PS4 Remote PKG, and PS5 ShadowMountPlus manager" \
      org.opencontainers.image.source=$SOURCE \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$COMMIT

ENV PS3MGR_PS3_GAME_DIR=/games \
	PS3MGR_PS2_GAME_DIR=/data/ps2 \
	PS3MGR_PS2_SYSTEM_DIR=/data/ps2/system \
	PS3MGR_PS2_USB_MOUNT_ROOT=/mnt/usb \
	PS3MGR_PS2_COVER_DOWNLOAD=true \
	PS3MGR_PS2_FPKG_EMULATOR=/data/JakV2.pkg \
	PS3MGR_PS4_GAME_DIR=/data/ps4 \
	PS3MGR_PS4_RPI_PORT=12800 \
	PS3MGR_PS4_PKG_LISTEN=0.0.0.0:8081 \
	PS3MGR_PS5_GAME_DIR=/data/ps5 \
	PS3MGR_PS5_REMOTE_GAME_DIR=/data/etaHEN/games \
	PS3MGR_PS5_FTP_PORT=2121 \
	PS3MGR_LISTEN=0.0.0.0:8080

COPY --from=build /out/ps3mgr /usr/local/bin/ps3mgr

EXPOSE 8080 8081
VOLUME ["/games", "/data", "/data/ps2", "/data/ps4", "/data/ps5", "/mnt/usb"]

ENTRYPOINT ["/usr/local/bin/ps3mgr"]
CMD ["serve"]
