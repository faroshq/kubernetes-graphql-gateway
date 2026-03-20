# Dev Environment Setup

## Prerequisites

- Go 1.25+
- A running Kubernetes cluster (kind, k3s, or any standard cluster)
- `kubectl` configured with access to that cluster

## Install tooling

```bash
# Task runner (used instead of Make)
go install github.com/go-task/task/v3/cmd/task@latest

# Or via package manager (macOS)
brew install go-task/tap/go-task
```

## Build

```bash
go build ./...
```

## Lint

```bash
task lint
```

## Test

```bash
task test
```

## Running locally (two-process mode)

The gateway has two components that run as separate processes:

**1. Listener** — watches a Kubernetes cluster and writes OpenAPI schema files to `_output/schemas/`:

```bash
task listener
```

Environment variables (set in `.taskenv` or `.secret/.env`):

| Variable | Description |
|---|---|
| `KUBECONFIG` | Path to kubeconfig for the cluster to watch |
| `ENABLE_KCP` | `true` for kcp multi-cluster mode, `false` (default) for standard Kubernetes |

**2. Gateway** — reads schema files and exposes them as a GraphQL endpoint on `:8080`:

```bash
task gateway
```

Then open the GraphQL playground: http://localhost:8080/api?

## Docker

```bash
task docker
# docker build -t ghcr.io/platform-mesh/kubernetes-graphql-gateway .
```

## Architecture (v2)

```
cmd/
  gateway/    — gateway binary entrypoint
  listener/   — listener binary entrypoint

apis/         — shared API types (v1alpha1: ClusterAccess, ClusterMetadata)
gateway/      — GraphQL gateway (schema generation, resolver, HTTP handler)
listener/     — cluster watcher, schema file writer
watcher/      — shared fsnotify-based file watcher
providers/    — kcp provider for multi-cluster mode
```

## v2 TODOs (from TODO file)

1. Authentication cleanup in listener
2. Gateway remote-cluster mux handler (`/api/clusters/{name}/graphql` + `/api/remote-clusters/{name}/graphql`)
3. Impersonation middleware (read `Impersonate-User` / `Impersonate-Group` headers)
4. No ENV-driven behaviour — config struct only
5. Contextual logging everywhere
6. Request traces + access logs (duration, status code)
