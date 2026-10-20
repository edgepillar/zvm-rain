# syntax=docker/dockerfile:1.7
#
# The Telegram bot. The contract is compiled into the binary (internal/
# contracts embeds the artifact) and the deployment record is public, so both
# are in the image; the operator's key is not, and is mounted at run time from
# secrets/ (docker-compose.yml, beside this file). Every setting comes from
# .env, so the command is just "bot".

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
# modernc.org/sqlite is pure Go, so the binary is static and the image needs no
# libc.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/zvm-rain ./cmd/zvm-rain

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /out/zvm-rain /usr/local/bin/zvm-rain
# Who is linked to which address, and every rain. The chain holds the money,
# so losing this loses nobody's tokens; people would have to /link again.
VOLUME /data
ENTRYPOINT ["zvm-rain"]
CMD ["bot"]
