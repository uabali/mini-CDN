# Mini CDN

Mini CDN is a small CDN simulation built with Go and Docker Compose.

The project has one origin server, three edge servers, and one router. When a client asks for a file, the router sends the request to an edge server. If the file is already in the edge cache, the edge returns it from disk. If not, the edge downloads it from the origin server, saves it in cache, and returns it to the client.

## Tech Stack

- Go
- Docker Compose
- File system cache
- Prometheus
- Grafana

## Features

- Origin server for source files
- Three edge cache nodes
- Round-robin routing
- Cache hit and miss headers
- TTL-based cache
- LRU cache eviction
- Manual cache purge
- Prometheus metrics
- Grafana dashboard

## Run

```bash
docker compose up --build
```

The router runs on:

```txt
http://localhost:8080
```

Grafana runs on:

```txt
http://localhost:3000
```

Default Grafana login:

```txt
admin / admin
```

## Test

Request the demo file:

```bash
curl -i http://localhost:8080/images/test.png
```

Look at these headers:

```txt
X-Cache: MISS
X-Edge-Node: edge-1
X-Router-Strategy: round-robin
```

Run the same command several times. The router will send requests to different edge nodes. After each edge caches the file, you should see:

```txt
X-Cache: HIT
```

Clear all edge caches:

```bash
curl -X POST -i http://localhost:8080/purge
```

Run tests:

```bash
go test ./...
```

If Go is not installed, run tests with Docker:

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.22-alpine go test ./...
```
