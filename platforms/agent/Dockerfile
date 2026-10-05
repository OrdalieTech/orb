# One team agent per container: `orb chat` runs the agent and starts buzz-acp,
# which connects it to Buzz. See README.md. Build from this directory.
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

# Not distroless: the agent's bash tool needs a shell and the tools it runs.
# The .deb binaries need glibc 2.39+.
FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends bash ca-certificates curl git \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --uid 10001 --home-dir /agent --no-create-home agent \
 && useradd --uid 10002 --create-home --groups agent buzz \
 && install -d -o agent -g agent /agent /agent/workspace \
 && install -d -o agent -g agent -m 0750 /run/orb
COPY --from=fetch /out/orb /deb/usr/bin/buzz-acp /usr/local/bin/
# The shell's buzz is orb, which asks the agent to run the real CLI with the
# Buzz key. Execute-only, the CLI runs non-dumpable: its environment stays
# unreadable to the agent's other processes.
COPY --from=fetch --chmod=0111 /deb/usr/bin/buzz /usr/local/libexec/buzz
RUN ln -s orb /usr/local/bin/buzz
COPY --chmod=0755 entrypoint /usr/local/bin/agent-entrypoint
ENV HOME=/agent \
    PI_CODING_AGENT_DIR=/agent/config \
    ORB_STATE_HOME=/agent/state \
    ORB_BUZZ_CLI=/usr/local/libexec/buzz \
    ORB_ACP_SOCKET=/run/orb/acp.sock \
    BUZZ_ACP_AGENT_COMMAND=/usr/local/bin/orb \
    BUZZ_ACP_AGENT_ARGS=chat,connect,/run/orb/acp.sock \
    BUZZ_ACP_NO_MEMORY=true
WORKDIR /agent/workspace
VOLUME /agent
# Starts as root only to run the agent and buzz-acp as two users (entrypoint).
ENTRYPOINT ["agent-entrypoint"]
CMD ["buzz", "telegram", "--tools"]
