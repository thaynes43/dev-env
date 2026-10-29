# Internal read-only portal. Build and publish in GitHub Actions, never in the
# shared dev-env pod. The tag is sha-<short> from publish.yml. This file does
# not assign a digest.
#
#   docker build -f images/portal/Dockerfile --build-arg COMMIT=$(git rev-parse HEAD) .

FROM golang:1.27.3@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b AS build
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

FROM gcr.io/distroless/static:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
COPY --from=build /src/bin/dev-env-portal /usr/local/bin/dev-env-portal
COPY LICENSE images/THIRD_PARTY.md /usr/share/doc/dev-env/
COPY --from=build /out/licenses/ /usr/share/licenses/dev-env/
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/dev-env-portal"]
