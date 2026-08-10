FROM golang:1.25-bookworm AS build-base

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

FROM build-base AS build-gateway
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/slack-copilot-gateway ./gateway/cmd/gateway

FROM build-base AS build-worker
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/slack-copilot-worker ./worker/cmd/worker

FROM build-base AS build-observability
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/slack-copilot-observability ./observability/cmd/observability

FROM build-base AS build-cli
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/slack-copilot ./cli/cmd/slack-copilot

FROM debian:bookworm-slim AS runtime-minimal

WORKDIR /app
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates \
	&& rm -rf /var/lib/apt/lists/* \
	&& useradd --create-home --uid 10001 --shell /usr/sbin/nologin slackcopilot
ENV HTTP_ADDR=:8080
EXPOSE 8080
USER slackcopilot

FROM runtime-minimal AS gateway
COPY --from=build-gateway /out/slack-copilot-gateway /app/slack-copilot-gateway
ENTRYPOINT ["/app/slack-copilot-gateway"]

FROM runtime-minimal AS runtime-worker
USER root
RUN apt-get update \
	&& apt-get install -y --no-install-recommends curl git openssh-client ripgrep \
	&& rm -rf /var/lib/apt/lists/*
USER slackcopilot

FROM runtime-worker AS worker
COPY --from=build-worker /out/slack-copilot-worker /app/slack-copilot-worker
ENTRYPOINT ["/app/slack-copilot-worker"]

FROM runtime-minimal AS observability
COPY --from=build-observability /out/slack-copilot-observability /app/slack-copilot-observability
ENTRYPOINT ["/app/slack-copilot-observability"]
