# ---- build stage ----
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/feedwatch ./cmd/feedwatch

# ---- runtime stage ----
FROM alpine:3.21
# ca-certificates is required for HTTPS to github.com and api.resend.com;
# tzdata makes the TZ environment variable affect timestamps in emails.
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S app \
 && adduser -S -G app app
ENV TZ=Asia/Shanghai
WORKDIR /app
COPY --from=build /out/feedwatch /app/feedwatch
# Placeholder default baked in so the image builds from a clean checkout:
# config.yaml is gitignored. docker-compose mounts the real config.yaml
# read-only, so editing feeds never requires rebuilding the image.
COPY config.example.yaml /app/config.yaml
RUN mkdir -p /app/data && chown -R app:app /app
USER app
ENV FEEDWATCH_STATE_PATH=/app/data/state.json \
    FEEDWATCH_LOG_FORMAT=json \
    FEEDWATCH_CONFIG=/app/config.yaml
# The seen-ID state MUST live on this volume, otherwise every container restart
# looks like a first run and re-sends the baseline email.
VOLUME ["/app/data"]
ENTRYPOINT ["/app/feedwatch"]
CMD ["-config", "/app/config.yaml"]
