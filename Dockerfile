FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/oncall-agent ./cmd/oncall-agent

FROM debian:bookworm-slim

WORKDIR /app
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates curl git openssh-client ripgrep \
	&& rm -rf /var/lib/apt/lists/* \
	&& useradd --create-home --uid 10001 --shell /usr/sbin/nologin oncall
COPY --from=build /out/oncall-agent /app/oncall-agent
COPY prompts /app/prompts

ENV HTTP_ADDR=:8080
EXPOSE 8080
USER oncall

ENTRYPOINT ["/app/oncall-agent"]
