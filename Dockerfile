# Multi-stage build: compile in a full Go toolchain, ship only the binary.
#
#   docker build -t webapp:local .
#   docker run --rm -p 8080:8080 webapp:local
#
# The result is roughly 10 MB and contains no shell, no package manager, and no
# libc. That is not just size — there is nothing in the final image for an
# attacker who achieves code execution to actually run.

FROM golang:1.25 AS build

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

# CGO_ENABLED=0 produces a statically linked binary. This is mandatory here:
# the distroless/static base has no libc, so a dynamically linked binary would
# build fine and then fail at startup with a confusing "no such file or
# directory" — the missing file being the linker, not the binary.
#
# -w -s strip DWARF and the symbol table, cutting several MB.
RUN CGO_ENABLED=0 GOOS=linux go build \
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
