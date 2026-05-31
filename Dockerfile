# --- build: pure-Go static binary (no cgo, thanks to modernc.org/sqlite) ---
# Runs on the builder's native platform and cross-compiles to the target, so
# multi-arch builds don't pay the QEMU tax for the Go compile.
# NOTE: keep this >= the `go` directive in go.mod (currently 1.25.x), or the build
# fails with "go.mod requires go >= ..." (the image pins GOTOOLCHAIN=local).
FROM --platform=$BUILDPLATFORM golang:1.25 AS build
WORKDIR /src

# Cache deps first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags="-s -w \
      -X github.com/prashar32/riskkernel/internal/version.Version=${VERSION} \
      -X github.com/prashar32/riskkernel/internal/version.Commit=${COMMIT} \
      -X github.com/prashar32/riskkernel/internal/version.Date=${DATE}" \
    -o /out/riskkernel ./cmd/riskkernel

# A /data dir owned by the nonroot uid so the default run works without a mount.
RUN mkdir -p /data && chown 65532:65532 /data

# --- runtime: distroless static, nonroot ---
FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/prashar32/riskkernel"
LABEL org.opencontainers.image.description="RiskKernel — the deterministic reliability runtime for AI agents"
LABEL org.opencontainers.image.licenses="Apache-2.0"

COPY --from=build /out/riskkernel /usr/local/bin/riskkernel
COPY --from=build --chown=65532:65532 /data /data

ENV RISKKERNEL_DATA_DIR=/data
EXPOSE 7070
VOLUME ["/data"]
USER 65532:65532

# distroless has no shell/curl, so the binary probes itself (GET /healthz).
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/usr/local/bin/riskkernel", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/riskkernel"]
CMD ["serve"]
