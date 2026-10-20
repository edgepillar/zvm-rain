# syntax=docker/dockerfile:1.7
#
# The page at /rain/ on a ZVM gateway. Built from the repository root, not from
# web/, because the page imports the deployment record in deployments/. The
# devnet's gateway forwards /rain/* here unchanged, so the build keeps its /rain/ base
# and the files sit under /srv/rain.

FROM node:22-alpine AS build
WORKDIR /src
COPY deployments ./deployments
COPY web/package.json web/package-lock.json ./web/
RUN --mount=type=cache,target=/root/.npm cd web && npm ci
COPY web ./web
# What the page needs from .env: the node it reads, where the explorer is, the
# token names and the bot's username. They arrive as build arguments rather
# than with the rest of .env, which holds the Telegram token and never enters
# this image.
ARG VITE_ZVM_NODE=
ARG VITE_GATEWAY=
ARG RAIN_TOKENS=
ARG RAIN_BOT_USERNAME=
ENV VITE_ZVM_NODE=$VITE_ZVM_NODE VITE_GATEWAY=$VITE_GATEWAY RAIN_TOKENS=$RAIN_TOKENS RAIN_BOT_USERNAME=$RAIN_BOT_USERNAME BASE_PATH=/rain/
RUN cd web && npx tsc && npx vite build

FROM caddy:2.10-alpine
COPY web/Caddyfile /etc/caddy/Caddyfile
COPY --from=build /src/web/dist /srv/rain
