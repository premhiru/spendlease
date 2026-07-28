# Two stages: build a fully static binary, then ship it on a minimal base.
# CGO is disabled throughout — the SQLite driver is pure Go (modernc.org/sqlite)
# precisely so this image needs no libc and no build toolchain at runtime.

FROM golang:1.25-alpine AS build

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


FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="spendlease" \
      org.opencontainers.image.description="Spend authorization gateway for AI agents" \
      org.opencontainers.image.source="https://github.com/premhiru/spendlease" \
      org.opencontainers.image.licenses="Apache-2.0"

COPY --from=build /out/spendlease /usr/local/bin/spendlease

# State lives here so it can be bind-mounted or backed by a volume. The image
# runs unprivileged; the directory is owned by the nonroot user.
WORKDIR /data
VOLUME /data

USER nonroot:nonroot
EXPOSE 4000

ENTRYPOINT ["/usr/local/bin/spendlease"]
CMD ["serve", "--addr", ":4000", "--store", "/data/spendlease.db"]
