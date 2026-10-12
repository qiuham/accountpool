FROM node:22-bookworm-slim AS web-build
WORKDIR /web
COPY --from=frontend package.json package-lock.json ./
RUN npm ci
COPY --from=frontend . .
RUN npm run build

FROM golang:1.26.5-bookworm AS build
ARG GOPROXY=https://proxy.golang.org
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN GOMAXPROCS=2 CGO_ENABLED=0 go build -p 2 -trimpath -o /accountpool ./cmd/accountpool

FROM python:3.13-slim-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates xvfb xauth libgtk-3-0 libdbus-glib-1-2 libasound2 \
    libx11-xcb1 libxt6 libnss3 libgbm1 fonts-liberation \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY executor/requirements.txt /tmp/requirements.txt
RUN python -m venv /app/.venv-stable \
    && /app/.venv-stable/bin/pip install --no-cache-dir -r /tmp/requirements.txt \
    && useradd --create-home --uid 1000 app \
    && mkdir -p /app/backend /app/artifacts /app/bundles /app/.subsets \
    && chown -R app:app /app/backend /app/artifacts /app/bundles /app/.subsets
ENV ACCOUNTPOOL_ROOT=/app ACCOUNTPOOL_DB=/app/backend/pool.db PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
USER app
# Same engine version as the accepted local browser; fetched at build time.
RUN /app/.venv-stable/bin/python -m camoufox fetch official/stable/152.0.4-beta.31 \
    && /app/.venv-stable/bin/python -m camoufox set official/stable/152.0.4-beta.31 \
    && /app/.venv-stable/bin/python -c "from camoufox.multiversion import load_config; from camoufox.pkgman import camoufox_path, Version; p=camoufox_path(download_if_missing=False); assert load_config().get('pinned') == '152.0.4-beta.31'; assert Version.from_path(p).version == '152.0.4'; assert (p / 'camoufox').is_file()"
COPY --from=build /accountpool /app/bin/accountpool
COPY --from=web-build /web/dist /app/web
COPY --chown=app:app executor/ /app/executor/
COPY config.example.toml /app/config.toml
COPY deploy/docker-entrypoint.sh /usr/local/bin/accountpool-entrypoint
ENTRYPOINT ["accountpool-entrypoint"]
CMD ["serve", "--addr", "0.0.0.0:8787"]
