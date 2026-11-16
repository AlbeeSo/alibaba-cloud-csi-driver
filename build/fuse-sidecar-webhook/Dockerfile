FROM --platform=$BUILDPLATFORM registry-cn-hangzhou.ack.aliyuncs.com/dev/golang:1.26.10 AS builder
WORKDIR /src
ARG TARGETOS=linux
ARG TARGETARCH=amd64
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/fuse-sidecar-webhook ./cmd/fuse-sidecar-webhook
FROM scratch
COPY --from=builder /out/fuse-sidecar-webhook /fuse-sidecar-webhook
USER 65532:65532
ENTRYPOINT ["/fuse-sidecar-webhook"]
