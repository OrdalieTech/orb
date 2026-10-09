# One team agent per container: orb-agent sets it up from /agent/agent.yaml and
# runs `orb chat` with the sidecars its platforms declare. See README.md. Build
# from this directory, one image per variant:
#
#   docker build --platform linux/amd64 --build-arg ORB_VERSION=<release> -t orb-agent platforms/agent
#   … --target browser -t orb-agent:browser                          agent-browser, Lightpanda
#   … --target browser --build-arg CHROMIUM=1 -t orb-agent:chromium  and headless Chromium
FROM debian:trixie-slim AS fetch
ARG ORB_VERSION
ARG BUZZ_VERSION=0.5.26
ARG BUZZ_SHA256=9c64119401e8d8cd56535fdb66eef8a944a4aece233b5c7ddf7dcd6ed3bb6697
ARG AGENT_BROWSER_VERSION=0.38.2
ARG AGENT_BROWSER_SHA256=2bb1d6e4660b2a109c912c8bb552f727125dbcd681c6d1eefc1af573b4546c49
ARG LIGHTPANDA_VERSION=1.0.0
ARG LIGHTPANDA_SHA256=aa5a4b8ed53d1e38b3c73f5b2647d0a84a82e6744557f45f9a9c85858aa031c3
ADD https://github.com/OrdalieTech/orb/releases/download/v${ORB_VERSION}/orb_${ORB_VERSION}_linux_amd64.tar.gz /orb.tar.gz
ADD https://github.com/OrdalieTech/orb/releases/download/v${ORB_VERSION}/checksums.txt /checksums.txt
ADD https://github.com/block/buzz/releases/download/desktop-v${BUZZ_VERSION}/Buzz_${BUZZ_VERSION}_amd64.deb /buzz.deb
ADD https://registry.npmjs.org/agent-browser/-/agent-browser-${AGENT_BROWSER_VERSION}.tgz /agent-browser.tgz
ADD https://github.com/lightpanda-io/browser/releases/download/${LIGHTPANDA_VERSION}/lightpanda-x86_64-linux /lightpanda
RUN apt-get update && apt-get install -y --no-install-recommends binutils \
 && echo "$(grep " orb_${ORB_VERSION}_linux_amd64.tar.gz$" /checksums.txt | cut -d' ' -f1)  /orb.tar.gz" | sha256sum -c \
 && echo "${BUZZ_SHA256}  /buzz.deb" | sha256sum -c \
 && echo "${AGENT_BROWSER_SHA256}  /agent-browser.tgz" | sha256sum -c \
 && echo "${LIGHTPANDA_SHA256}  /lightpanda" | sha256sum -c \
 && mkdir -p /out /browser && tar -xzf /orb.tar.gz -C /out orb \
 && dpkg-deb -x /buzz.deb /deb \
 && tar -xzf /agent-browser.tgz -C /browser --strip-components=1 --no-same-permissions package/bin/agent-browser-linux-x64 package/skills package/skill-data \
 && chmod -R a+rX /browser && chmod 0755 /browser/bin/agent-browser-linux-x64 \
 && strip -o /out/lightpanda /lightpanda && chmod 0755 /out/lightpanda

# orb-agent, from the release's own source.
FROM golang:1.27 AS harness
ARG ORB_VERSION
ADD https://github.com/OrdalieTech/orb/archive/refs/tags/v${ORB_VERSION}.tar.gz /orb-source.tar.gz
RUN mkdir /src && tar -xzf /orb-source.tar.gz -C /src --strip-components=1 \
 && cd /src && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /orb-agent ./platforms/agent/harness

# Not distroless: the agent's bash tool needs a shell and the tools it runs,
# git and curl included; the .deb binaries need glibc 2.39+. Git's few Perl
# commands (send-email, svn) go with Perl; apt goes after use.
FROM debian:trixie-slim AS rootfs
ARG CHROMIUM=
RUN apt-get update \
 && apt-get install -y --no-install-recommends bash ca-certificates curl git netcat-openbsd \
    ${CHROMIUM:+chromium-headless-shell fonts-liberation} \
 && dpkg --purge --force-depends --force-remove-protected perl perl-modules-5.40 libperl5.40 liberror-perl apt libapt-pkg7.0 \
 && rm -rf /var/lib/apt/lists/* /var/cache/* /var/log/* /usr/share/man /usr/share/info /usr/share/locale /usr/share/lintian \
 && find /usr/share/doc -type f ! -name copyright -delete && find /usr/share/doc -type d -empty -delete \
 && useradd --uid 10001 --home-dir /agent --no-create-home agent \
 && useradd --uid 10002 --create-home --groups agent sidecar \
 && install -d -o agent -g agent /agent /agent/workspace \
 && install -d -o agent -g agent -m 0750 /run/orb

FROM scratch AS agent
COPY --from=rootfs / /
COPY --from=fetch /out/orb /deb/usr/bin/buzz-acp /usr/local/bin/
COPY --from=harness /orb-agent /usr/local/bin/
# The shell's buzz is orb, which asks the agent to run the real CLI with the
# Buzz key. Execute-only, the CLI runs non-dumpable: its environment stays
# unreadable to the agent's other processes.
COPY --from=fetch --chmod=0111 /deb/usr/bin/buzz /usr/local/libexec/buzz
RUN ln -s orb /usr/local/bin/buzz
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    HOME=/agent \
    ORB_AGENT_DIR=/agent/config \
    ORB_STATE_HOME=/agent/state \
    ORB_BUZZ_CLI=/usr/local/libexec/buzz
WORKDIR /agent/workspace
VOLUME /agent
# Starts as root only to run the agent and its sidecars as their own users.
ENTRYPOINT ["orb-agent"]
CMD ["buzz", "telegram", "--tools"]

# agent-browser with Lightpanda, and Chromium when built with CHROMIUM=1; the
# agent file's `browser:` picks the engine.
FROM agent AS browser
COPY --from=fetch /browser /usr/local/lib/agent-browser
COPY --from=fetch /out/lightpanda /usr/local/bin/
RUN ln -s ../lib/agent-browser/bin/agent-browser-linux-x64 /usr/local/bin/agent-browser

FROM agent
