FROM --platform=$BUILDPLATFORM node:24.11.1-alpine3.22 AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS server-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=web-build /src/web/dist ./web/dist
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags='-s -w' -o /out/network-observer ./cmd/observer

FROM alpine:3.22.2
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S observer \
    && adduser -S -D -H -G observer observer
WORKDIR /app
COPY --from=server-build /out/network-observer /usr/local/bin/network-observer
COPY --from=server-build /src/web/dist /app/web/dist
ENV WEB_DIST_DIR=/app/web/dist
USER observer
EXPOSE 8080 8443 8445
ENTRYPOINT ["/usr/local/bin/network-observer"]
