FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/slack-copilot-agent ./cmd/slack-copilot-agent
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/slack-copilot-gateway ./gateway/cmd/gateway
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/slack-copilot-worker ./worker/cmd/worker
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/slack-copilot-observability ./observability/cmd/observability
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/slack-copilot ./cli/cmd/slack-copilot

FROM debian:bookworm-slim

WORKDIR /app
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates curl git openssh-client ripgrep \
	&& rm -rf /var/lib/apt/lists/* \
	&& useradd --create-home --uid 10001 --shell /usr/sbin/nologin slackcopilot
COPY --from=build /out/slack-copilot-agent /app/slack-copilot-agent
COPY --from=build /out/slack-copilot-gateway /app/slack-copilot-gateway
COPY --from=build /out/slack-copilot-worker /app/slack-copilot-worker
COPY --from=build /out/slack-copilot-observability /app/slack-copilot-observability
COPY --from=build /out/slack-copilot /app/slack-copilot

ENV HTTP_ADDR=:8080
EXPOSE 8080
USER slackcopilot

ENTRYPOINT ["/app/slack-copilot-agent"]
