FROM golang:1.27.4-alpine3.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/network-observer-agent ./cmd/agent

FROM alpine:3.22.2
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S observer \
    && adduser -S -D -H -G observer observer \
    && mkdir -p /etc/network-observer /var/lib/network-observer \
    && chown -R observer:observer /etc/network-observer /var/lib/network-observer
COPY --from=build /out/network-observer-agent /usr/local/bin/network-observer-agent
ENTRYPOINT ["/usr/local/bin/network-observer-agent"]
