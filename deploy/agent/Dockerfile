FROM --platform=$BUILDPLATFORM alpine:3.22.2 AS ebpf-build
RUN apk add --no-cache clang llvm linux-headers libbpf-dev
WORKDIR /src
COPY internal/agent/bpf/flow_counter.c ./flow_counter.c
ARG TARGETARCH
RUN case "$TARGETARCH" in amd64) arch=x86 ;; arm64) arch=arm64 ;; *) echo "unsupported TARGETARCH=$TARGETARCH" >&2; exit 1 ;; esac && clang -O2 -g -target bpf -D__TARGET_ARCH_${arch} -I/usr/include -c flow_counter.c -o /flow_counter.o

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags='-s -w' -o /out/network-observer-agent ./cmd/agent

FROM alpine:3.22.2
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S observer \
    && adduser -S -D -H -G observer observer \
    && mkdir -p /etc/network-observer /var/lib/network-observer \
    && chown -R observer:observer /etc/network-observer /var/lib/network-observer
COPY --from=build /out/network-observer-agent /usr/local/bin/network-observer-agent
COPY --from=ebpf-build /flow_counter.o /usr/local/lib/network-observer/flow_counter.o
ENTRYPOINT ["/usr/local/bin/network-observer-agent"]
