FROM golang:1.24-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM gcr.io/distroless/base-debian12:nonroot

WORKDIR /
COPY --from=builder /out/server /server

ENV APP_ENV=production
ENV PORT=8080
ENV SQLITE_PATH=/data/backend.sqlite

VOLUME ["/data"]
EXPOSE 8080

ENTRYPOINT ["/server"]
