# canon-proxy

`canon-proxy` is a high-performance Go proxy for the Canon EOS 2000D WiFi HTTP interface. It continuously polls the camera for new images and uploads each new file to a configurable backend.

## Features

- Poll Canon EOS 2000D WiFi HTTP interface for image URLs
- Detect only new images (in-memory seen set)
- Parallel download/upload pipeline using worker pool
- **Web UI** for reviewing detected images and selectively pushing to storage
- Pluggable upload backends:
  - SMB
  - FTP
  - AWS S3
  - Azure Blob Storage
  - Google Cloud Storage
- Graceful shutdown via SIGINT/SIGTERM

## Modes

### `auto` (default)
Every image detected on the camera is downloaded and uploaded to the configured backend immediately — the original behaviour.

### `manual`
Images are detected and shown in the web UI. You review the thumbnails, select which ones you want, and click **Push selected** or **Push all pending**. Nothing is uploaded until you explicitly request it.

## Architecture

```mermaid
flowchart TD
    Camera["Canon EOS 2000D<br/>WiFi HTTP endpoint"]
    Client["Canon HTTP Client"]
    Poller["Poller<br/>(new images only)"]

    subgraph auto["auto mode"]
      WorkerPool["Worker Pool<br/>download+upload"]
    end

    subgraph manual["manual mode"]
      Store["Image Store"]
      WebUI["Web UI / API<br/>:9090"]
      Workers["Worker Pool<br/>download+upload"]
    end

    SMB["SMB"]
    FTP["FTP"]
    S3["S3"]
    AzureGCS["Azure/GCS"]

    Camera <-->|poll| Client
    Client --> Poller
    Poller --> WorkerPool
    Poller --> Store
    Store <--> WebUI
    WebUI -->|push request| Workers
    WorkerPool --> SMB
    WorkerPool --> FTP
    WorkerPool --> S3
    WorkerPool --> AzureGCS
    Workers --> SMB
    Workers --> FTP
    Workers --> S3
    Workers --> AzureGCS
```

## Build

```bash
go mod tidy
go build ./...
```

## Run

```bash
go run ./cmd/canon-proxy --config config.yaml
```

## Docker

```bash
docker build -t canon-proxy .
docker run --rm -v "$(pwd)/config.example.yaml:/app/config.yaml:ro" -p 9090:9090 canon-proxy
```

## Configuration

Copy `config.example.yaml` to `config.yaml` and adjust values.

```yaml
camera:
  host: "192.168.1.100"
  port: 8080
  poll_interval: 5s

upload:
  workers: 4
  backend: smb # smb | ftp | s3 | azure | gcs

web:
  listen: ":9090"  # address for the web UI
  mode: manual     # manual | auto

backends:
  smb:
    host: "192.168.1.10"
    share: "photos"
    username: "user"
    password: "pass"
    path: "/uploads"
  ftp:
    host: "ftp.example.com"
    port: 21
    username: "user"
    password: "pass"
    tls: false
    path: "/uploads"
  s3:
    bucket: "my-bucket"
    region: "eu-west-1"
    prefix: "canon/"
    access_key: ""
    secret_key: ""
  azure:
    account: "mystorageaccount"
    container: "photos"
    prefix: "canon/"
    sas_token: ""
  gcs:
    bucket: "my-bucket"
    prefix: "canon/"
    credentials_file: "/path/to/sa.json"
```

## Web UI

When `web.mode` is `manual`, open `http://<host>:9090` in a browser after starting canon-proxy.

- Thumbnail grid auto-refreshes every 4 seconds
- Check images you want to upload, then click **Push selected**
- Or click **Push all pending** to enqueue everything at once
- Status badges show `pending` / `uploading` / `done` / `failed` per image

