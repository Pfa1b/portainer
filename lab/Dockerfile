# Retain the exact release's frontend and ancillary tools; replace only the server.
FROM portainer/portainer-ce:2.39.1@sha256:1ae8e65d50ca5498cb2c33e617495a1e3ef245b0d2392b4a44c70ae09b822891
ARG PATCH_COMMIT
COPY dist/portainer /portainer
LABEL org.opencontainers.image.revision=$PATCH_COMMIT \
      io.bayai.upstream.commit=2c1f3c27d5b817ba405f44efe225972e789c2ec1 \
      io.bayai.purpose=isolated-laboratory
