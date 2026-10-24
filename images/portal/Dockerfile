# Internal read-only portal. Build and publish in GitHub Actions, never in the
# shared dev-env pod. The tag is sha-<short> from publish.yml. This file does
# not assign a digest.
#
#   docker build -f images/portal/Dockerfile --build-arg COMMIT=$(git rev-parse HEAD) .

FROM golang:1.27.2@sha256:820153096fda4de6ac1b7194d12e416b7dd8f4e9a33d17e0345b111b945162d8 AS build
WORKDIR /src
# renovate: datasource=github-releases depName=google/go-licenses
ARG GO_LICENSES_VERSION=v2.0.1
RUN CGO_ENABLED=0 go install "github.com/google/go-licenses/v2@${GO_LICENSES_VERSION}"
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG COMMIT=
ARG VERSION=
RUN make build-portal COMMIT="${COMMIT}" VERSION="${VERSION}"
RUN mkdir -p /out/licenses/go-stdlib \
 && CGO_ENABLED=0 go-licenses save ./cmd/dev-env-portal \
      --ignore github.com/thaynes43/dev-env --save_path=/out/licenses/go-modules \
 && mkdir -p /out/licenses/go-modules \
 && cp "$(go env GOROOT)/LICENSE" /out/licenses/go-stdlib/LICENSE

FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /src/bin/dev-env-portal /usr/local/bin/dev-env-portal
COPY LICENSE images/THIRD_PARTY.md /usr/share/doc/dev-env/
COPY --from=build /out/licenses/ /usr/share/licenses/dev-env/
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/dev-env-portal"]
