# Deployment agent

The deployment agent runs on a Docker host. It reads every container and every image from the local Docker socket, then pulls and recreates workloads. It does not open port 22, it does not SSH, and it does not publish a port.

The daemon is the source of truth. There is no target list and no registry list. A container created after the agent starts shows up through the Docker event stream and is included on the next pass.

## What it asks

On the first start, in a terminal, the agent prints the inventory and asks which way to pull:

1. **Compose file.** Type a path, or pick a compose file already recorded on a running project. The agent pulls the images named in that file from whatever registries those references use, then runs `docker compose up -d --wait --pull never`.
2. **Watchtower.** The agent watches every running container whose image has a tag. It polls that image's registry and recreates the container when the manifest digest changes. New containers join on their own. Digest-pinned images are shown and left alone. Stopped containers are shown and are not started.

The agent asks for a yes before the first pull. The answer is saved in the state directory so a later restart, including a container restart with no terminal, continues the same mode. `--reconfigure` asks again.

Private registries use the Docker credential store. A 401 asks once for a username and token for that host. Docker Hub, GHCR, Amazon ECR, Google Artifact Registry, and any other registry are chosen from the image reference. ECR uses the AWS default credential chain. Artifact Registry uses application default credentials.

## Run

```shell
go build -o bin/agent ./cmd/agent
./bin/agent
```

Flags:

| Flag | Default | Purpose |
| --- | --- | --- |
| `--state-dir` | `/var/lib/deployment-agent` (user cache on Windows) | Session and rollback memory |
| `--interval` | `5m` | Time between passes |
| `--docker-host` | `DOCKER_HOST` or the local socket | Daemon address |
| `--listen` | `127.0.0.1:9090` | Local `/healthz` only |
| `--compose-bin` | `docker-compose` | Compose v2 binary |
| `--name` | `deployment-agent` | This agent's container name |
| `--reconfigure` | off | Ask for the mode again |

The health address refuses anything other than loopback.

## As a container

```shell
docker compose -f deployments/docker-compose.yaml run --rm deployment-agent
```

After the session is saved:

```shell
docker compose -f deployments/docker-compose.yaml up -d
```

The socket is mounted and no ports are published. Compose mode resolves bind paths in the agent process, so mount each managed project directory at the same absolute path the daemon host uses.

## Updates and rollback

A change is a new manifest digest. The agent pulls `name@sha256:…`, moves the tag onto that image, and recreates. One container is updated at a time. When the container has a Docker healthcheck, the agent waits for `healthy`. Otherwise it waits until the container has stayed running. A failure renames the previous container back and starts it. That digest is remembered and is not retried until the registry moves the tag again.

Watchtower mode can include this agent only if you answer yes. The old process keeps running until the replacement is up. If the replacement fails, the old container is renamed back and keeps serving.

The Docker socket is root-equivalent. The agent is an administrator of that daemon.

## Develop

```shell
go test ./...
go test -tags integration ./...
```

The integration tests talk to a local daemon and skip when Docker is not available.
