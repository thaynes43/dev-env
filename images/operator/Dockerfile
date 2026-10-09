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
# publish.yml builds this file on every merge to main that changes a build input and
# pushes ghcr.io/thaynes43/dev-env-operator:sha-<short>, signed with keyless cosign.

FROM golang:1.27.2@sha256:5bc7f572bbaa98885a3a1fd9c0aa76b59e3e14e8628bfc316bbfd0c701e4818c AS build
WORKDIR /src
# go-licenses collects the license texts of the modules linked into the binaries (#55).
# It goes in before the sources, so a Go change does not rebuild it.
# renovate: datasource=github-releases depName=google/go-licenses
ARG GO_LICENSES_VERSION=v2.0.1
RUN CGO_ENABLED=0 go install "github.com/google/go-licenses/v2@${GO_LICENSES_VERSION}"
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG COMMIT=
ARG VERSION=
# `make build` is the same path CI and developers use (static, trimpath, stamped).
RUN make build COMMIT="${COMMIT}" VERSION="${VERSION}"
# The texts of the third-party modules the two binaries link, plus the Go standard
# library's own license. `--ignore` skips this repo's own packages (MIT, the LICENSE
# copied beside them below). CGO_ENABLED=0 matches the Makefile, so go-licenses sees
# the package set the binaries were built from. go-licenses refuses an existing save
# path and may write none when no third-party module is linked, hence the late mkdir.
RUN mkdir -p /out/licenses/go-stdlib \
 && CGO_ENABLED=0 go-licenses save ./cmd/dev-env-operator ./cmd/dev-env-keeper \
      --ignore github.com/thaynes43/dev-env --save_path=/out/licenses/go-modules \
 && mkdir -p /out/licenses/go-modules \
 && cp "$(go env GOROOT)/LICENSE" /out/licenses/go-stdlib/LICENSE

FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /src/bin/dev-env-operator /src/bin/dev-env-keeper /usr/local/bin/
# This repo's license, the third-party list and the module license texts (#55). The
# distroless base keeps its Debian packages' copyright files in /usr/share/doc.
COPY LICENSE images/THIRD_PARTY.md /usr/share/doc/dev-env/
COPY --from=build /out/licenses/ /usr/share/licenses/dev-env/
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/dev-env-operator"]
