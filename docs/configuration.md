---
icon: lucide/settings
---

# Configuration

## Config file

`config.yaml` is a **first-run seed**. Values are written to the SQLite database on the initial start. After that the **database is the source of truth** — changes to `config.yaml` are ignored unless you delete the database.

All settings are also editable live from the **Settings** page in the web UI.

---

## Top-level sections

```yaml
database:
  driver: sqlite          # sqlite | postgres | mssql
  dsn: ./canon-proxy.db   # file path for sqlite; full DSN for others

web:
  listen: ":9090"         # address for the web UI / REST API
```

---

## Camera

```yaml
camera:
  host: "192.168.2.70"   # IP address of the camera
  port: 15740             # PTP/IP port — do not change for Canon
  poll_interval: 5s       # how often to check for new images
```

!!! warning "GUID pairing"
    The camera enforces GUID-based pairing. The GUID is embedded in the binary at compile time. Once paired, a different client GUID (even one byte different) results in `InitFail 0x00000001`. Do not modify the GUID unless you intend to re-pair.

---

## Upload

```yaml
upload:
  workers: 1              # parallel upload workers
  backend: smb            # smb | ftp | s3 | azure | gcs
```

| Field | Description |
|---|---|
| `workers` | Number of concurrent upload goroutines |
| `backend` | Which storage backend to use |

---

## Backends

### SMB

```yaml
backends:
  smb:
    host: "192.168.2.9"
    share: "photos"
    username: "user"
    password: "secret"
    path: "/uploads"
```

### FTP

```yaml
backends:
  ftp:
    host: "ftp.example.com"
    port: 21
    username: "user"
    password: "secret"
    tls: false
    path: "/uploads"
```

### AWS S3

```yaml
backends:
  s3:
    bucket: "my-bucket"
    region: "eu-west-1"
    prefix: "camera/"
    access_key: "AKIA..."
    secret_key: "..."
```

### Azure Blob Storage

```yaml
backends:
  azure:
    account: "mystorageaccount"
    container: "photos"
    prefix: "camera/"
    sas_token: "..."
```

### Google Cloud Storage

```yaml
backends:
  gcs:
    bucket: "my-gcs-bucket"
    credentials_file: "/secrets/gcs-key.json"
    path_prefix: "camera/"
```

---

## Database

```yaml
database:
  driver: sqlite                   # sqlite (default) | postgres | mssql
  dsn: ./canon-proxy.db            # SQLite: relative path
  # dsn: "host=db user=cp password=secret dbname=canon sslmode=disable"
```

!!! note
    Changing the driver after first run requires migrating data. For most home-lab deployments SQLite is sufficient.

---

## Environment variable overrides

Environment variable overrides are not currently implemented.

Use the Settings page (persisted in SQLite) or provide values in `config.yaml` on first run.
