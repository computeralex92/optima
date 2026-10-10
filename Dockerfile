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
# Fully static binary: the Go runtime, cgo, HiGHS and libstdc++/libc/zlib are all
# linked in, so the runtime stage needs no libraries at all. Static glibc means no
# NSS/DNS, which is fine here because the solver resolves no names.
RUN CGO_ENABLED=1 go build \
        -ldflags '-linkmode external -extldflags "-static"' \
        -o /out/optimizer ./cmd/optimizer

# scratch: nothing but the static binary. UID 65534 (nobody) is numeric because
# scratch has no /etc/passwd.
FROM scratch
COPY --from=build /out/optimizer /optimizer
EXPOSE 7050
USER 65534:65534
ENTRYPOINT ["/optimizer"]
