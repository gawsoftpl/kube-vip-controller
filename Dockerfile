# Etap 1: Build
FROM golang:1.24-alpine AS builder

# Instalacja wymaganych pakietów (libsodium-dev + git)
RUN apk add --no-cache git build-base

# Ustaw katalog roboczy
WORKDIR /app

# Skopiuj go.mod i go.sum (dla cache)
COPY go.mod go.sum ./

# Pobierz zależności
RUN go mod download

# Skopiuj resztę źródeł
COPY . .

# Build statycznej binarki
RUN CGO_ENABLED=1 GOOS=linux go build -o kube-vip-webhook main.go

# Etap 2: Minimalny runtime
FROM alpine:latest

# Skopiuj binarkę z etapu build
COPY --from=builder /app/kube-vip-webhook /usr/local/bin/kube-vip-webhook

# Ustaw entrypoint
ENTRYPOINT ["/usr/local/bin/kube-vip-webhook"]

