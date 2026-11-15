# One team agent per container: orb-agent sets it up from /agent/agent.yaml and
# runs `orb chat`, one process for every platform. See README.md. Build
# from this directory, one image per variant:
#
#   docker build --platform linux/amd64 --build-arg ORB_VERSION=<release> -t orb-agent platforms/agent
#   … --target browser -t orb-agent:browser                          agent-browser, Lightpanda
#   … --target browser --build-arg CHROMIUM=1 -t orb-agent:chromium  and headless Chromium
FROM debian:trixie-slim AS fetch
ARG ORB_VERSION
ARG AGENT_BROWSER_VERSION=0.38.2
ARG AGENT_BROWSER_SHA256=2bb1d6e4660b2a109c912c8bb552f727125dbcd681c6d1eefc1af573b4546c49
ARG LIGHTPANDA_VERSION=1.0.0
ARG LIGHTPANDA_SHA256=aa5a4b8ed53d1e38b3c73f5b2647d0a84a82e6744557f45f9a9c85858aa031c3
ADD https://github.com/OrdalieTech/orb/releases/download/v${ORB_VERSION}/orb_${ORB_VERSION}_linux_amd64.tar.gz /orb.tar.gz
ADD https://github.com/OrdalieTech/orb/releases/download/v${ORB_VERSION}/checksums.txt /checksums.txt
ADD https://registry.npmjs.org/agent-browser/-/agent-browser-${AGENT_BROWSER_VERSION}.tgz /agent-browser.tgz
ADD https://github.com/lightpanda-io/browser/releases/download/${LIGHTPANDA_VERSION}/lightpanda-x86_64-linux /lightpanda
RUN apt-get update && apt-get install -y --no-install-recommends binutils \
 && echo "$(grep " orb_${ORB_VERSION}_linux_amd64.tar.gz$" /checksums.txt | cut -d' ' -f1)  /orb.tar.gz" | sha256sum -c \
 && echo "${AGENT_BROWSER_SHA256}  /agent-browser.tgz" | sha256sum -c \
 && echo "${LIGHTPANDA_SHA256}  /lightpanda" | sha256sum -c \
 && mkdir -p /out /browser && tar -xzf /orb.tar.gz -C /out orb \
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
# git and curl included. Git's few Perl commands (send-email, svn) go with
# Perl; apt goes after use.
FROM debian:trixie-slim AS rootfs
ARG CHROMIUM=
RUN apt-get update \
 && apt-get install -y --no-install-recommends bash ca-certificates curl git \
    ${CHROMIUM:+chromium-headless-shell fonts-liberation} \
 && dpkg --purge --force-depends --force-remove-protected perl perl-modules-5.40 libperl5.40 liberror-perl apt libapt-pkg7.0 \
 && rm -rf /var/lib/apt/lists/* /var/cache/* /var/log/* /usr/share/man /usr/share/info /usr/share/locale /usr/share/lintian \
 && find /usr/share/doc -type f ! -name copyright -delete && find /usr/share/doc -type d -empty -delete \
 && useradd --uid 10001 --home-dir /agent --no-create-home agent \
 && install -d -o agent -g agent /agent /agent/workspace \
 && install -d -o agent -g agent -m 0750 /run/orb

FROM scratch AS agent
COPY --from=rootfs / /
COPY --from=fetch /out/orb /usr/local/bin/
COPY --from=harness /orb-agent /usr/local/bin/
# ORB_CONFIG_FILES: every orb here, `docker exec … orb plugins list` included,
# reads the settings.json and models.json orb-agent renders, as the agent does.
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    HOME=/agent \
    ORB_AGENT_DIR=/agent/config \
    ORB_STATE_HOME=/agent/state \
    ORB_CONFIG_FILES=1
WORKDIR /agent/workspace
VOLUME /agent
# Starts as root only to hand the agent its credentials on descriptors.
ENTRYPOINT ["orb-agent"]
CMD ["buzz", "telegram", "--tools"]

# agent-browser with Lightpanda, and Chromium when built with CHROMIUM=1; the
# agent file's `browser:` picks the engine.
FROM agent AS browser
COPY --from=fetch /browser /usr/local/lib/agent-browser
COPY --from=fetch /out/lightpanda /usr/local/bin/
RUN ln -s ../lib/agent-browser/bin/agent-browser-linux-x64 /usr/local/bin/agent-browser

FROM agent
