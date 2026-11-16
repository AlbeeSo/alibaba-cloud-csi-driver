ARG DEMO_BASE_IMAGE=mirrors-ssl.aliyuncs.com/debian:bookworm-slim
FROM --platform=$BUILDPLATFORM registry-cn-hangzhou.ack.aliyuncs.com/dev/golang:1.26.10 AS builder
WORKDIR /src
ARG TARGETOS=linux
ARG TARGETARCH=amd64
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/csi-mount-proxy-server ./cmd/mount-proxy-server

FROM ${DEMO_BASE_IMAGE}
RUN apt-get update && apt-get install -y --no-install-recommends python3 python3-fusepy fuse ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /out/csi-mount-proxy-server /usr/local/bin/csi-mount-proxy-server
COPY examples/customfuse/6-file-sidecar/demo_fs.py /demo_fs.py
COPY --chmod=755 examples/customfuse/6-file-sidecar/entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/usr/local/bin/csi-mount-proxy-server"]
