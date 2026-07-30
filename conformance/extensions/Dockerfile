# Runner image for the ecosystem extension matrix.
#
# Node 24 is upstream Pi's own documented requirement and stays the reference
# engine. Bun is copied in from the pinned official image so the orb-bun tier
# can be forced through PATH inside the same networkless container; building it
# in here keeps the runtime container itself offline.
#
#   docker build -f conformance/extensions/Dockerfile -t orb-extension-matrix:24-bun1.3.14 conformance/extensions
#
# The image contains no corpus packages, no credentials and no repository code.
FROM oven/bun:1.3.14-slim AS bun

FROM node:24-bookworm-slim
COPY --from=bun /usr/local/bin/bun /usr/local/bin/bun
RUN node --version && bun --version
