# The operator image: dev-env-operator and dev-env-keeper (D-38), two static
# binaries in one distroless/nonroot image. Build from the repo root:
#
#   docker build -f images/operator/Dockerfile \
#     --build-arg COMMIT=$(git rev-parse HEAD) [--build-arg VERSION=v2.0.0] .
#
# The build context has no .git (.dockerignore), so neither `git describe` nor the
# Go toolchain's VCS stamp exists. Pass COMMIT always, and VERSION on a release
# build, or the binaries report "dev" (internal/version). Base images are tags for
# now; Renovate pins them to digests (B4).

FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG COMMIT=
ARG VERSION=
# `make build` is the same path CI and developers use (static, trimpath, stamped).
RUN make build COMMIT="${COMMIT}" VERSION="${VERSION}"

FROM gcr.io/distroless/static:nonroot
COPY --from=build /src/bin/dev-env-operator /src/bin/dev-env-keeper /usr/local/bin/
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/dev-env-operator"]
