# The operator image: dev-env-operator and dev-env-keeper (D-38), two static
# binaries in one distroless/nonroot image. Build from the repo root:
#
#   docker build -f images/operator/Dockerfile \
#     --build-arg COMMIT=$(git rev-parse HEAD) [--build-arg VERSION=v2.0.0] .
#
# The build context has no .git (.dockerignore), so neither `git describe` nor the
# Go toolchain's VCS stamp exists. Pass COMMIT always, and VERSION on a release
# build, or the binaries report "dev" (internal/version). Base images are pinned by
# digest; Renovate (B4) keeps the digests fresh.

FROM golang:1.27.1@sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG COMMIT=
ARG VERSION=
# `make build` is the same path CI and developers use (static, trimpath, stamped).
RUN make build COMMIT="${COMMIT}" VERSION="${VERSION}"

FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /src/bin/dev-env-operator /src/bin/dev-env-keeper /usr/local/bin/
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/dev-env-operator"]
