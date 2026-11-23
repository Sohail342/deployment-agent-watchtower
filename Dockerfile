FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/agent ./cmd/agent

FROM debian:bookworm-slim
ARG COMPOSE_VERSION=v2.32.4
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates curl \
	&& curl -fsSL -o /usr/local/bin/docker-compose \
		"https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-linux-$(uname -m)" \
	&& chmod 0755 /usr/local/bin/docker-compose \
	&& apt-get purge -y curl \
	&& apt-get autoremove -y \
	&& rm -rf /var/lib/apt/lists/*
COPY --from=build /out/agent /usr/local/bin/agent
USER root
ENTRYPOINT ["agent"]
CMD ["--state-dir", "/var/lib/deployment-agent"]
