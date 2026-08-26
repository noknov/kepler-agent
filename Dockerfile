FROM golang:1.25-bookworm AS build-base

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

FROM golang:1.25-bookworm AS build-gopls
RUN mkdir -p /out && GOBIN=/out CGO_ENABLED=0 go install golang.org/x/tools/gopls@v0.20.0

FROM build-base AS build-gateway
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/kepler-agent-gateway ./gateway/cmd/gateway

FROM build-base AS build-worker
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/kepler-agent-worker ./worker/cmd/worker

FROM build-base AS build-observability
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/kepler-agent-observability ./observability/cmd/observability

FROM build-base AS build-cli-bin
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/kepler-agent ./cli/cmd/kepler-agent

# Keep the benchmark-export target artifact-only. Docker's local exporter writes
# the whole target filesystem, so exporting directly from build-base would also
# attempt to materialize the source tree on the host.
FROM scratch AS build-cli
COPY --from=build-cli-bin /out/kepler-agent /kepler-agent

FROM debian:bookworm-slim AS runtime-minimal

WORKDIR /app
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates \
	&& rm -rf /var/lib/apt/lists/* \
	&& useradd --create-home --uid 10001 --shell /usr/sbin/nologin kepleragent
ENV HTTP_ADDR=:8080
EXPOSE 8080
USER kepleragent

FROM runtime-minimal AS gateway
COPY --from=build-gateway /out/kepler-agent-gateway /app/kepler-agent-gateway
ENTRYPOINT ["/app/kepler-agent-gateway"]

FROM runtime-minimal AS runtime-worker
USER root
RUN apt-get update \
	&& apt-get install -y --no-install-recommends curl git openssh-client ripgrep \
	&& rm -rf /var/lib/apt/lists/*
COPY --from=build-gopls /out/gopls /usr/local/bin/gopls
USER kepleragent

FROM runtime-worker AS worker
COPY --from=build-gopls /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}"
COPY --from=build-worker /out/kepler-agent-worker /app/kepler-agent-worker
ENTRYPOINT ["/app/kepler-agent-worker"]

FROM runtime-minimal AS observability
COPY --from=build-observability /out/kepler-agent-observability /app/kepler-agent-observability
ENTRYPOINT ["/app/kepler-agent-observability"]
