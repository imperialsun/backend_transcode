FROM golang:1.25.7 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN mkdir -p /out /runtime-data && \
    go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM gcr.io/distroless/base-debian12:nonroot

WORKDIR /
COPY --from=builder /out/server /server
COPY --from=builder --chown=nonroot:nonroot /runtime-data /data

ENV APP_ENV=production
ENV PORT=8080
ENV SQLITE_PATH=/data/backend.sqlite

EXPOSE 8080

ENTRYPOINT ["/server"]
