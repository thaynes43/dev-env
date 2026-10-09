# Login-only keeper helper (D-78). It runs no daemon or agent and receives only
# private tmpfs staging/control; no CA, App, ServiceAccount or provider-home mount.
# Build/publish in GitHub Actions, never in the shared dev-env pod.
FROM golang:1.27.1@sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2 AS build
WORKDIR /src
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

FROM node:24-slim@sha256:d6aa754f16b3197301076f047b5def2f02ea1dbbc2ca920407d46d7ec7f87b20
# Deliberately fixed to the source contract reviewed in D-78, not an auto-upgrade.
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
