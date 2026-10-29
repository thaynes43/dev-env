# Login-only keeper helper (D-78). It runs no daemon or agent and receives only
# private tmpfs staging/control; no CA, App, ServiceAccount or provider-home mount.
# Build/publish in GitHub Actions, never in the shared dev-env pod.
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
RUN make build COMMIT="${COMMIT}" VERSION="${VERSION}"
RUN mkdir -p /out/licenses/go-stdlib \
 && CGO_ENABLED=0 go-licenses save ./cmd/dev-env-keeper \
      --ignore github.com/thaynes43/dev-env --save_path=/out/licenses/go-modules \
 && cp "$(go env GOROOT)/LICENSE" /out/licenses/go-stdlib/LICENSE

FROM node:26-slim@sha256:e4db4c914bf8ad241bd24804fc25d272d89bad97e68cdc8a1414862790a02c39
# D-78 requires a pinned, reviewed helper; coordinate version changes with the
# agent guard and login smoke check. Proposals are not an automatic upgrade.
# renovate: datasource=npm depName=@openai/codex
ARG CODEX_VERSION=0.160.1
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/* \
 && curl -fsSLo /tmp/codex-install.sh \
      "https://github.com/openai/codex/releases/download/rust-v${CODEX_VERSION}/install.sh" \
 && mkdir /tmp/codex-home \
 && HOME=/tmp/codex-home CODEX_HOME=/opt/dev-env/codex CODEX_INSTALL_DIR=/usr/local/bin \
      CODEX_NON_INTERACTIVE=1 sh /tmp/codex-install.sh --release "${CODEX_VERSION}" \
 && chmod -R a+rX /opt/dev-env/codex \
 && rm -rf /tmp/codex-install.sh /tmp/codex-home \
 && mkdir -p /usr/share/licenses/codex \
 && curl -fsSLo /usr/share/licenses/codex/LICENSE \
      "https://raw.githubusercontent.com/openai/codex/rust-v${CODEX_VERSION}/LICENSE" \
 && curl -fsSLo /usr/share/licenses/codex/NOTICE \
      "https://raw.githubusercontent.com/openai/codex/rust-v${CODEX_VERSION}/NOTICE" \
 && codex --version
COPY --from=build /src/bin/dev-env-keeper /usr/local/bin/
COPY --from=build /out/licenses/ /usr/share/licenses/dev-env/
COPY LICENSE images/THIRD_PARTY.md /usr/share/doc/dev-env/
ARG COMMIT=
ARG VERSION=
LABEL org.opencontainers.image.source="https://github.com/thaynes43/dev-env" \
      org.opencontainers.image.title="dev-env Codex login helper" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.version="${VERSION}"
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/dev-env-keeper", "codex-login-idle"]
