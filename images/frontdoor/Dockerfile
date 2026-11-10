# Meatball's front door. Build and publish in GitHub Actions, never in the
# shared dev-env pod. The tag is sha-<short> from publish.yml. This file does
# not assign a digest.
#
#   docker build -f images/frontdoor/Dockerfile --build-arg COMMIT=$(git rev-parse HEAD) .
#
# The runtime is Python so opwork can run. The process does not call the
# Kubernetes API: it presents one projected ServiceAccount token to the operator.

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
RUN make build-frontdoor COMMIT="${COMMIT}" VERSION="${VERSION}"
RUN mkdir -p /out/licenses/go-stdlib \
 && CGO_ENABLED=0 go-licenses save ./cmd/dev-env-frontdoor \
      --ignore github.com/thaynes43/dev-env --save_path=/out/licenses/go-modules \
 && mkdir -p /out/licenses/go-modules \
 && cp "$(go env GOROOT)/LICENSE" /out/licenses/go-stdlib/LICENSE

# Index digest of python:3.13-slim-bookworm, so amd64 and arm64 both resolve.
FROM python:3.13-slim-bookworm@sha256:a5a7017efc5969e00ae2b91c8eb0631049a569652aa4ffc4501769049ae45c72
RUN groupadd --system --gid 65532 nonroot \
 && useradd --system --uid 65532 --gid 65532 --create-home --home-dir /home/nonroot --shell /usr/sbin/nologin nonroot
COPY --from=build /src/bin/dev-env-frontdoor /usr/local/bin/dev-env-frontdoor
COPY images/agent/tools/openproject/opwork.py images/agent/tools/openproject/agent-api.py /usr/local/lib/opwork/
COPY images/agent/tools/opwork.sh /usr/local/bin/opwork
RUN chmod 0755 /usr/local/bin/dev-env-frontdoor /usr/local/bin/opwork \
 && chmod 0644 /usr/local/lib/opwork/*.py
COPY LICENSE images/THIRD_PARTY.md /usr/share/doc/dev-env/
COPY --from=build /out/licenses/ /usr/share/licenses/dev-env/
ENV HOME=/home/nonroot
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/dev-env-frontdoor"]
