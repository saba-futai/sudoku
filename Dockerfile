# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS builder

WORKDIR /src
RUN apk add --no-cache ca-certificates git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags "-s -w" -o /out/sudoku ./cmd/sudoku-tunnel

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 sudoku && mkdir -p /etc/sudoku && chown -R sudoku:sudoku /etc/sudoku

COPY --from=builder /out/sudoku /usr/local/bin/sudoku
COPY scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 755 /usr/local/bin/docker-entrypoint.sh

USER sudoku

# Default ports (override via config + docker run -p)
EXPOSE 8080 8081

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["-c", "/etc/sudoku/server.config.json"]
