# Fixture-only Linux AMD64 image. Hosted build only; system Git and v1 unchanged.
ARG BASE_IMAGE=ghcr.io/thaynes43/dev-env:2.11.0@sha256:98761577d27feded51427a8097a9cf9492c2e7ee62b6e9a99df27bf52017532d
FROM ${BASE_IMAGE} AS builder
USER root
ARG COMMIT
ARG BASE_IMAGE
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential dpkg-dev patch zlib1g-dev libcurl4-gnutls-dev \
      libexpat1-dev libpcre2-dev gettext && rm -rf /var/lib/apt/lists/*
COPY images/git-wait /recipe
COPY scripts/trials /fixture-checks
RUN env BASE_IMAGE="${BASE_IMAGE}" nice -n 19 python3 /recipe/prepare-source.py /build/native && \
    env BASE_IMAGE="${BASE_IMAGE}" nice -n 19 python3 /recipe/build.py /build/native /build/out

FROM ${BASE_IMAGE}
ARG COMMIT
LABEL org.opencontainers.image.source="https://github.com/thaynes43/dev-env" \
      org.opencontainers.image.revision="${COMMIT}"
USER root
COPY --from=builder /build/out/git-wait /build/out/git-control /build/out/libgit-wait-reader.so /build/out/marker-test /build/out/git-wait-provenance.json /opt/dev-env/trials/
COPY --from=builder /build/out/git-wait-source.tar.gz /opt/dev-env/trials/
COPY --from=builder /build/out/git-wait-LICENSE /usr/share/licenses/git-wait/COPYING
COPY images/THIRD_PARTY.md /usr/share/doc/dev-env/THIRD_PARTY.md
COPY scripts/trials /opt/dev-env/trials/checks/
COPY images/git-wait /opt/dev-env/trials/recipe/
USER dev
WORKDIR /tmp
ENTRYPOINT ["/usr/bin/false"]
