---
icon: lucide/camera
---

# canon-proxy

**canon-proxy** is a lightweight Go service that connects to a Canon EOS camera over WiFi using the PTP/IP protocol, continuously discovers new images, and uploads them to configurable storage backends.

---

## Key Features

| Feature | Details |
|---|---|
| **Protocol** | PTP/IP over TCP :15740 — same pairing as Canon's own apps |
| **Discovery** | Delta polling — only new images are processed each cycle |
| **Web UI** | Thumbnail browser, manual push controls, settings editor |
| **Upload Modes** | `auto` (hands-free) · `manual` (review before upload) |
| **Backends** | SMB · FTP · AWS S3 · Azure Blob · Google Cloud Storage |
| **Video** | MOV files detected; displayed with a ▶ badge in the UI |
| **Resilience** | Exponential back-off reconnect, delete-after-upload option |
| **Container** | Multi-arch image (`linux/amd64`, `linux/arm64`) on GHCR |
| **Helm chart** | Published to OCI (`ghcr.io/pacorreia/charts/canon-proxy`) |

---

## Quick Start

```bash
# 1. Copy the example config
cp config.example.yaml config.yaml
# Edit camera.host to your camera's IP

# 2. Run
go run ./cmd/canon-proxy --config config.yaml

# 3. Open the UI
open http://localhost:9090
```

See [Getting Started](getting-started.md) for full setup instructions.

---

## Project Links

- **Source**: [github.com/pacorreia/canon-proxy](https://github.com/pacorreia/canon-proxy)
- **Container registry**: `ghcr.io/pacorreia/canon-proxy`
- **Helm chart**: `oci://ghcr.io/pacorreia/charts/canon-proxy`
