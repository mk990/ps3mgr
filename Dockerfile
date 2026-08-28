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

FROM gcr.io/distroless/static-debian12:nonroot

ARG VERSION=dev
ARG COMMIT=unknown
ARG SOURCE=https://github.com/unknown/ps3mgr

LABEL org.opencontainers.image.title="PlayStation Manager" \
	  org.opencontainers.image.description="PS2 OPL/USB, PS3 FTP, and PS5 ShadowMountPlus manager with independent queues" \
      org.opencontainers.image.source=$SOURCE \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$COMMIT

ENV PS3MGR_GAME_DIR=/games \
	PS3MGR_PS2_GAME_DIR=/data/ps2 \
	PS3MGR_PS2_SYSTEM_DIR=/data/ps2/system \
	PS3MGR_PS2_USB_MOUNT_ROOT=/mnt/usb \
	PS3MGR_PS2_COVER_DOWNLOAD=true \
	PS3MGR_PS5_GAME_DIR=/data/ps5 \
	PS3MGR_PS5_REMOTE_GAME_DIR=/data/etaHEN/games \
	PS3MGR_PS5_FTP_PORT=2121 \
	PS3MGR_LISTEN=0.0.0.0:8080

COPY --from=build /out/ps3mgr /usr/local/bin/ps3mgr

EXPOSE 8080
VOLUME ["/games", "/data/ps2", "/data/ps5", "/mnt/usb"]

ENTRYPOINT ["/usr/local/bin/ps3mgr"]
CMD ["serve"]
