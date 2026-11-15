ARG GO_IMAGE_VERSION=1.26-alpine
ARG DISTRO_IMAGE_VERSION=debian12
FROM golang:${GO_IMAGE_VERSION} AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/canon-proxy ./cmd/canon-proxy

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
WORKDIR /app
COPY --from=builder /out/canon-proxy ./canon-proxy
# /data is the recommended mount point for the SQLite database.
# /app/config.yaml should be mounted via a ConfigMap or bind-mount.
EXPOSE 9090
VOLUME ["/data"]
ENTRYPOINT ["./canon-proxy", "--config", "/app/config.yaml"]
