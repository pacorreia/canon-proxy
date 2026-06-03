---
icon: lucide/container
---

# Deployment

## Docker

### Default bridge network

The simplest way to run canon-proxy in Docker. Docker's iptables MASQUERADE rewrites the container source IP to the host's LAN IP, so the camera sees a same-subnet connection and accepts the PTP/IP handshake.

```bash
docker run --rm \
  # Ensure database.dsn points at /data/canon-proxy.db to persist state in the /data volume
  -v "$(pwd)/config.yaml:/app/config.yaml:ro" \
  -v canon-proxy-data:/data \
  -p 9090:9090 \
  ghcr.io/pacorreia/canon-proxy:latest
```

### Host network (alternative)

Use `--network host` if bridge NAT is not available (e.g. certain NAS environments or when the camera requires strict same-subnet enforcement):

```bash
docker run --rm \
  --network host \
  -v "$(pwd)/config.yaml:/app/config.yaml:ro" \
  -v canon-proxy-data:/data \
  ghcr.io/pacorreia/canon-proxy:latest
```

### Docker Compose

```yaml
services:
  canon-proxy:
    image: ghcr.io/pacorreia/canon-proxy:latest
    ports:
      - "9090:9090"
    volumes:
      - ./config.yaml:/app/config.yaml:ro
      - canon-proxy-data:/data
    restart: unless-stopped

volumes:
  canon-proxy-data:
```

---

## Kubernetes (Helm)

### Add the OCI chart

```bash
helm install canon-proxy \
  oci://ghcr.io/pacorreia/charts/canon-proxy \
  --version 0.1.0 \
  --namespace canon-proxy \
  --create-namespace \
  -f my-values.yaml
```

### Key values

```yaml
# my-values.yaml

config:
  camera:
    host: "192.168.2.70"
    port: 15740
    poll_interval: 5s
  upload:
    workers: 1
    backend: smb
  web:
    listen: ":9090"

# SMB credentials — stored in a Secret
secret:
  smb:
    password: "mysecretpassword"

# Persistent volume for the SQLite database
persistence:
  enabled: true
  size: 1Gi

# Traefik IngressRoute
ingress:
  enabled: true
  host: "canon-proxy.example.com"
  tls: true

# Set to true only if your camera is NOT reachable via the cluster CNI.
# When true, the pod shares the node's network namespace.
hostNetwork: false
```

!!! warning "hostNetwork and camera access"
    If the Canon camera is on the same LAN as your Kubernetes nodes but **not** reachable from the pod network (no routing / CNI isolation), set `hostNetwork: true` to share the node's network namespace. The camera will then see the node's real IP.

### Upgrade

```bash
helm upgrade canon-proxy \
  oci://ghcr.io/pacorreia/charts/canon-proxy \
  --version 0.2.0 \
  --reuse-values
```

---

## Building from source

```bash
git clone https://github.com/pacorreia/canon-proxy.git
cd canon-proxy

# Build for the local OS
CGO_ENABLED=0 go build -o canon-proxy ./cmd/canon-proxy

# Cross-compile for Linux ARM64
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o canon-proxy-linux-arm64 ./cmd/canon-proxy
```

### Build the Docker image

```bash
docker build -t canon-proxy:local .
```

The Dockerfile uses a **distroless nonroot** runtime image — no shell, minimal attack surface.
