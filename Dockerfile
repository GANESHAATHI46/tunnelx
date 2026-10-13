FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/tunnelx-server ./cmd/server

FROM alpine:3.21
RUN addgroup -S tunnelx && adduser -S -G tunnelx tunnelx && mkdir -p /var/lib/tunnelx && chown -R tunnelx:tunnelx /var/lib/tunnelx
COPY --from=build /out/tunnelx-server /usr/local/bin/tunnelx-server
USER tunnelx
ENV TUNNELX_LISTEN=0.0.0.0:8080 TUNNELX_DATA_DIR=/var/lib/tunnelx
EXPOSE 8080
VOLUME ["/var/lib/tunnelx"]
ENTRYPOINT ["/usr/local/bin/tunnelx-server"]
