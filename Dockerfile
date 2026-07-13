FROM golang:1.26.5 AS builder

WORKDIR /src

ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN mkdir -p /out /runtime-data && \
    go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM debian:bookworm-slim AS runtime

RUN apt-get update && \
    apt-get install -y --no-install-recommends ffmpeg ca-certificates && \
    rm -rf /var/lib/apt/lists/* && \
    useradd --system --uid 65532 --create-home --home-dir /home/nonroot nonroot

WORKDIR /
COPY --from=builder /out/server /server
COPY --from=builder --chown=nonroot:nonroot /runtime-data /data

ENV APP_ENV=production
ENV PORT=8080
ENV SQLITE_PATH=/data/backend.sqlite

EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/server"]
