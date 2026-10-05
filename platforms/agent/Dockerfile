# One team agent per container: `orb chat` runs the agent and starts buzz-acp,
# which connects it to Buzz. See README.md.
#
#   docker build --platform linux/amd64 --build-arg ORB_VERSION=<release> -t orb-agent platforms/agent
FROM debian:trixie-slim AS fetch
ARG ORB_VERSION
ARG BUZZ_VERSION=0.5.26
ARG BUZZ_SHA256=9c64119401e8d8cd56535fdb66eef8a944a4aece233b5c7ddf7dcd6ed3bb6697
ADD https://github.com/OrdalieTech/orb/releases/download/v${ORB_VERSION}/orb_${ORB_VERSION}_linux_amd64.tar.gz /orb.tar.gz
ADD https://github.com/OrdalieTech/orb/releases/download/v${ORB_VERSION}/checksums.txt /checksums.txt
ADD https://github.com/block/buzz/releases/download/desktop-v${BUZZ_VERSION}/Buzz_${BUZZ_VERSION}_amd64.deb /buzz.deb
RUN echo "$(grep " orb_${ORB_VERSION}_linux_amd64.tar.gz$" /checksums.txt | cut -d' ' -f1)  /orb.tar.gz" | sha256sum -c \
 && echo "${BUZZ_SHA256}  /buzz.deb" | sha256sum -c \
 && mkdir /out && tar -xzf /orb.tar.gz -C /out orb \
 && dpkg-deb -x /buzz.deb /deb

# Not distroless: the agent's tools (Orb's bash, buzz-dev-mcp's shell) and the
# buzz CLI it posts with all need a shell. The .deb binaries need glibc 2.39+.
FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends bash ca-certificates curl git \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --uid 10001 --home-dir /agent --no-create-home agent \
 && install -d -o agent -g agent /agent /agent/workspace
COPY --from=fetch /out/orb /deb/usr/bin/buzz-acp /deb/usr/bin/buzz-dev-mcp /deb/usr/bin/buzz /usr/local/bin/
USER agent
ENV HOME=/agent \
    PI_CODING_AGENT_DIR=/agent/config \
    ORB_STATE_HOME=/agent/state \
    BUZZ_ACP_MCP_COMMAND=/usr/local/bin/buzz-dev-mcp \
    BUZZ_ACP_NO_MEMORY=true
WORKDIR /agent/workspace
VOLUME /agent
ENTRYPOINT ["orb", "chat"]
CMD ["buzz", "telegram", "--tools"]
