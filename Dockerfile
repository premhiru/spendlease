# Two stages: build a fully static binary, then ship it on a minimal base.
# CGO is disabled throughout — the SQLite driver is pure Go (modernc.org/sqlite)
# precisely so this image needs no libc and no build toolchain at runtime.

FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

WORKDIR /src

# Copy manifests first so dependency download is cached independently of source.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /out/spendlease ./cmd/spendlease

# The final stage copies this empty directory with the runtime user's numeric
# ownership so SQLite can create its files there.
RUN mkdir -p /out/data


FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35

LABEL org.opencontainers.image.title="spendlease" \
      org.opencontainers.image.description="Spend authorization gateway for AI agents" \
      org.opencontainers.image.source="https://github.com/premhiru/spendlease" \
      org.opencontainers.image.licenses="Apache-2.0"

COPY --from=build /out/spendlease /usr/local/bin/spendlease
COPY --from=build --chown=65532:65532 /out/data /data

# State lives here so it can be bind-mounted or backed by a volume. The image
# runs unprivileged; the directory is owned by the nonroot user.
WORKDIR /data
VOLUME /data

USER nonroot:nonroot
EXPOSE 4000

ENTRYPOINT ["/usr/local/bin/spendlease"]
CMD ["serve", "--addr", ":4000", "--store", "/data/spendlease.db"]
