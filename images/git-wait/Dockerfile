# Fixture-only Linux AMD64 image. Hosted build only; system Git and v1 unchanged.
FROM ghcr.io/thaynes43/dev-env:2.9.1@sha256:a975de7dbc40c6f33048a38a327a2db2b9698a7615f5b2b9897abbb1d04df0cf AS builder
USER root
ARG COMMIT
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential dpkg-dev patch zlib1g-dev libcurl4-gnutls-dev \
      libexpat1-dev libpcre2-dev gettext && rm -rf /var/lib/apt/lists/*
COPY images/git-wait /recipe
COPY scripts/trials /fixture-checks
RUN nice -n 19 python3 /recipe/prepare-source.py /build/native && \
    nice -n 19 python3 /recipe/build.py /build/native /build/out

FROM ghcr.io/thaynes43/dev-env:2.9.1@sha256:a975de7dbc40c6f33048a38a327a2db2b9698a7615f5b2b9897abbb1d04df0cf
ARG COMMIT
LABEL org.opencontainers.image.source="https://github.com/thaynes43/dev-env" \
      org.opencontainers.image.revision="${COMMIT}"
USER root
COPY --from=builder /build/out/git-wait /build/out/git-control /build/out/libgit-wait-reader.so /build/out/marker-test /build/out/git-wait-provenance.json /opt/dev-env/trials/
COPY --from=builder /build/out/git-wait-source.tar.gz /opt/dev-env/trials/
COPY scripts/trials /opt/dev-env/trials/checks/
COPY images/git-wait /opt/dev-env/trials/recipe/
USER dev
WORKDIR /tmp
ENTRYPOINT ["/usr/bin/false"]
