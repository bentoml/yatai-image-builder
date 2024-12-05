FROM ghcr.io/liubin/toml-cli:v0.0.7 as toml

FROM golang:1.23 as builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY common/ common/
COPY bento-image-snapshotter/ bento-image-snapshotter/
COPY seekabletar/ seekabletar/

# Build
ARG VERSION_BUILDFLAGS

RUN echo "Building with flags: "${VERSION_BUILDFLAGS}" ..."
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "${VERSION_BUILDFLAGS}" -a -o /usr/bin/bento-image-snapshotter bento-image-snapshotter/main.go

FROM ubuntu:22.04

RUN apt-get update && apt-get install -y \
    curl \
    unzip \
    xz-utils \
    zstd \
    file \
    aria2 \
    && rm -rf /var/lib/apt/lists/*

RUN curl -L https://github.com/peak/s5cmd/releases/download/v2.2.2/s5cmd_2.2.2_Linux-64bit.tar.gz | tar zxv && mv ./s5cmd /usr/bin

COPY --from=builder /usr/bin/bento-image-snapshotter /usr/bin/bento-image-snapshotter
COPY --from=toml /bin/toml /usr/bin/toml
