# syntax=docker/dockerfile:1
# Two images from one file:
#   docker build --target herakles   -t herakles .
#   docker build --target eurystheus -t herakles-eurystheus .
# Both are a static binary on distroless, running as a non-root user.

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS base
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# Cross-compile on the build platform: no emulation for multi-arch images.
ARG TARGETOS TARGETARCH
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH

FROM base AS build-herakles
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags "-s -w" -o /out/herakles ./cmd/herakles

FROM base AS build-eurystheus
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags "-s -w" -o /out/eurystheus ./examples/eurystheus

FROM gcr.io/distroless/static-debian12:nonroot AS eurystheus
COPY --from=build-eurystheus /out/eurystheus /eurystheus
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/eurystheus"]

# Last stage: the default target.
FROM gcr.io/distroless/static-debian12:nonroot AS herakles
COPY --from=build-herakles /out/herakles /herakles
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/herakles"]
CMD ["run", "-config", "/etc/herakles/config/herakles.yaml"]
