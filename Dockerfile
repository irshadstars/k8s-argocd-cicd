# Multi-stage build: compile in a full Go toolchain, ship only the binary.
#
#   docker build -t webapp:local .
#   docker run --rm -p 8080:8080 webapp:local
#
# The result is roughly 10 MB and contains no shell, no package manager, and no
# libc. That is not just size — there is nothing in the final image for an
# attacker who achieves code execution to actually run.

# --platform=$BUILDPLATFORM pins the builder to the machine actually running the
# build (amd64 on GitHub runners) rather than the target platform. Combined with
# GOARCH below, this cross-compiles natively instead of emulating the target
# under QEMU — the same multi-arch result, roughly 5-10x faster.
FROM --platform=$BUILDPLATFORM golang:1.25 AS build

WORKDIR /src

# Copy go.mod on its own first so this layer caches independently of source
# changes. With no dependencies it does little today, but it means adding one
# later does not re-download the module graph on every code edit.
COPY app/go.mod ./
RUN go mod download

COPY app/ ./

# Build metadata, passed in by CI. Defaults keep a bare `docker build` working.
ARG VERSION=dev
ARG COMMIT=none
ARG BUILT=unknown

# Set automatically by buildx per target platform. Declaring them makes them
# visible to the RUN below; the defaults only apply to a plain `docker build`.
ARG TARGETOS=linux
ARG TARGETARCH

# CGO_ENABLED=0 produces a statically linked binary. This is mandatory here:
# the distroless/static base has no libc, so a dynamically linked binary would
# build fine and then fail at startup with a confusing "no such file or
# directory" — the missing file being the linker, not the binary.
#
# -w -s strip DWARF and the symbol table, cutting several MB.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -trimpath \
    -ldflags="-w -s -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.built=${BUILT}" \
    -o /out/webapp .

# ---

# :nonroot runs as UID 65532, matching runAsUser in manifests/deployment.yaml.
# Those two numbers must agree: the pod spec sets runAsNonRoot, so a mismatch
# means the kubelet refuses to start the container at all.
FROM gcr.io/distroless/static:nonroot

COPY --from=build /out/webapp /webapp

EXPOSE 8080
USER 65532:65532

ENTRYPOINT ["/webapp"]
