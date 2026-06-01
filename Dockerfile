FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/canon-proxy ./cmd/canon-proxy

FROM alpine:3.19
WORKDIR /app
COPY --from=builder /out/canon-proxy ./canon-proxy
COPY config.example.yaml ./config.yaml
ENTRYPOINT ["./canon-proxy", "--config", "/app/config.yaml"]
