# Build a static binary (pure-Go SQLite, no CGO) and ship it in a tiny image.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/maintenance-tracker .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates sqlite \
 && adduser -D -H -u 10001 app \
 && mkdir /data && chown app /data
COPY --from=build /out/maintenance-tracker /usr/local/bin/maintenance-tracker
USER app
ENV ADDR=:8080 DB_PATH=/data/maintenance.db
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["maintenance-tracker"]
