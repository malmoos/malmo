# malmo-ui control-plane image (BUILD.md # 5b; WEB_UI.md # deploy model).
# Build context is web-ui/:  docker build -f web-ui/Dockerfile -t malmo-ui:dev web-ui
#
#   build   — produce the static SPA bundle with Vite.
#   runtime — caddy:2-alpine serving the bundle from /srv/ui with the SPA
#             Caddyfile (fallback to index.html, compression + ETag on).

FROM node:20-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
# `vite build` directly, not `npm run build`: the image's job is to produce the
# bundle. Type-checking (vue-tsc) is a separate CI gate (make check-web), not a
# step in baking the artifact.
RUN npx vite build

FROM caddy:2-alpine AS runtime
COPY Caddyfile /etc/caddy/Caddyfile
COPY --from=build /app/dist /srv/ui
