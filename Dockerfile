FROM golang:1.23-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod ./
RUN go mod download
COPY . .
RUN go test ./...
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pyaterochka-vpn .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H -u 10001 app && mkdir -p /data && chown app:app /data
WORKDIR /app
COPY --from=build /out/pyaterochka-vpn /usr/local/bin/pyaterochka-vpn
USER app
ENV DATABASE_PATH=/data/data.sqlite
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/pyaterochka-vpn"]
