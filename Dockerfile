# One Dockerfile for all binaries:
#   docker build --build-arg APP=ovpn-agent --build-arg VERSION=0.1.6 -t vihat/ovpn-agent:0.1.6 .

# The dashboard page (React + HeroUI), embedded into ovpn-dash by go:embed.
# Static files are the same on every platform, so build them on the build machine's.
FROM --platform=$BUILDPLATFORM node:24-alpine AS ui
WORKDIR /ui
COPY cmd/ovpn-dash/ui/package.json cmd/ovpn-dash/ui/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY cmd/ovpn-dash/ui/ ./
RUN npm run build -- --outDir /web

# Country database shipped inside ovpn-dash: DB-IP "IP to Country Lite" (CC BY 4.0,
# https://db-ip.com). The month is pinned so a rebuild gives the same image; a newer file
# at the dash's data/geoip.mmdb wins over it at run time.
FROM --platform=$BUILDPLATFORM alpine:3.22 AS geoip
ARG GEOIP_MONTH=2026-11
RUN wget -qO- "https://download.db-ip.com/free/dbip-country-lite-${GEOIP_MONTH}.mmdb.gz" | gunzip > /dbip-country-lite.mmdb \
 && test -s /dbip-country-lite.mmdb

FROM golang:1.26.11-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /web ./cmd/ovpn-dash/web
COPY --from=geoip /dbip-country-lite.mmdb /tmp/
ARG APP
ARG VERSION=dev
RUN test -n "$APP" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid= -X main.version=$VERSION" -o /out/app ./cmd/$APP \
 && mkdir -p /out/data /out/geoip \
 && if [ "$APP" = ovpn-dash ]; then cp /tmp/dbip-country-lite.mmdb /out/geoip/; fi

FROM scratch
ARG APP
ARG VERSION=dev
LABEL org.opencontainers.image.title="$APP" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.description="OpenVPN who-is-online monitor"
COPY --from=build /out/app /app
# ovpn-dash only (empty for the others): the built-in country database.
COPY --from=build /out/geoip /usr/share/geoip
# Writable data dir for ovpn-dash history (a fresh named volume inherits this owner).
COPY --from=build --chown=65532:65532 /out/data /data
# SQLite puts temporary files (large sorts) here; the root filesystem is read-only.
ENV SQLITE_TMPDIR=/data
USER 65532:65532
ENTRYPOINT ["/app"]
