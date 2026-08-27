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

LABEL org.opencontainers.image.title="PS3 Game Manager" \
      org.opencontainers.image.description="A lightweight PS3 game library and FTP transfer manager" \
      org.opencontainers.image.source=$SOURCE \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$COMMIT

ENV PS3MGR_GAME_DIR=/games \
    PS3MGR_LISTEN=0.0.0.0:8080

COPY --from=build /out/ps3mgr /usr/local/bin/ps3mgr

EXPOSE 8080
VOLUME ["/games"]

ENTRYPOINT ["/usr/local/bin/ps3mgr"]
CMD ["serve"]
