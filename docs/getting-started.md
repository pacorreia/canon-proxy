---
icon: lucide/rocket
---

# Getting Started

## Prerequisites

| Requirement | Minimum |
|---|---|
| Go | 1.24 |
| Canon EOS camera | WiFi / PTP/IP enabled |
| Storage backend | SMB share, FTP server, S3 bucket, Azure Blob, or GCS bucket |

## 1. Enable WiFi on the camera

On a Canon EOS 2000D (and most modern EOS bodies):

1. **Menu → WiFi/Bluetooth → WiFi function → Other cameras / Connect**  
2. Note the **SSID** and connect your machine to that network, or join the same LAN if using infrastructure mode.

The camera listens on **TCP port 15740** (PTP/IP).

## 2. Install from source

```bash
git clone https://github.com/pacorreia/canon-proxy.git
cd canon-proxy
go build -o canon-proxy ./cmd/canon-proxy
```

## 3. Configure

```bash
cp config.example.yaml config.yaml
```

Edit `config.yaml` — at minimum set `camera.host` to your camera's IP:

```yaml
camera:
  host: "192.168.2.70"   # your camera's IP
  port: 15740
  poll_interval: 5s
```

!!! tip "Settings are stored in the database"
    After the first run, all settings are persisted in the SQLite database and editable live from the **Settings** page in the web UI. Changes to `config.yaml` are ignored once a setting exists in the DB.

## 4. Run

```bash
./canon-proxy --config config.yaml
```

Open **http://localhost:9090** — you should see the web UI with thumbnails appearing as the initial sync runs.

## 5. Configure a storage backend

Go to **Settings** in the web UI and configure your preferred backend. For example, to use an SMB share:

| Field | Value |
|---|---|
| Backend | `smb` |
| Host | `192.168.2.9` |
| Share | `photos` |
| Username | `user` |
| Password | `secret` |
| Path | `/uploads` |

## 6. Queue images for upload

Images are discovered automatically and shown in the web UI. Select the ones you want and click **Queue selected** or **Queue all** to start uploading.

Uploads start as soon as an image is queued.

## Next Steps

- [Configuration reference](configuration.md)
- [Architecture overview](architecture.md)
- [Docker & Kubernetes deployment](deployment.md)
