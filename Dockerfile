# syntax=docker/dockerfile:1

# Builder: Debian-based Go image with a C++ toolchain and zlib headers.
# gohighs is a cgo binding that links a static HiGHS (C++), so CGO_ENABLED=1,
# g++ and zlib1g-dev are required (its linux link flags include -lstdc++ -lz).
FROM golang:1.27-bookworm AS build
RUN apt-get update && apt-get install -y --no-install-recommends \
        g++ zlib1g-dev \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o /out/optimizer ./cmd/optimizer

# Runtime: HiGHS needs glibc and libstdc++, so not scratch. Debian bookworm-slim
# matches the builder's glibc.
FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates libstdc++6 zlib1g \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/optimizer /usr/local/bin/optimizer
EXPOSE 7050
USER nobody
ENTRYPOINT ["/usr/local/bin/optimizer"]
