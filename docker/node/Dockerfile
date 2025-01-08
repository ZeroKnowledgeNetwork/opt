FROM ubuntu:latest AS base

# Install run-time dependencies
RUN \
  --mount=type=cache,target=/var/cache/apt \
  --mount=type=cache,target=/var/lib/apt \
  apt-get update && apt-get install -y --no-install-recommends \
  ca-certificates \
  && rm -rf /var/lib/apt/lists/*


FROM base AS builder

ARG ENABLE_WARPED_EPOCH=false
ARG KATZENPOST_DIR=/src/katzenpost
ARG VERSION_GO=1.22.3

ENV GOROOT=/usr/local/go
ENV PATH=$GOROOT/bin:$PATH
ENV GOCACHE=/root/.cache/go-build
ENV GO_BUILD_OPTS="-trimpath -ldflags=-buildid= -ldflags=-X=github.com/katzenpost/katzenpost/core/epochtime.WarpedEpoch=${ENABLE_WARPED_EPOCH}"

# Install build dependencies
RUN \
  --mount=type=cache,target=/var/cache/apt \
  --mount=type=cache,target=/var/lib/apt \
  apt-get update && apt-get install -y --no-install-recommends \
  build-essential \
  git \
  wget

# Install Go
RUN f=go${VERSION_GO}.linux-amd64.tar.gz \
  && wget https://dl.google.com/go/${f} \
  && tar -C /usr/local -xzf ${f} \
  && rm ${f}

# Copy the project source
COPY . /src

# Build Katzenpost components
RUN --mount=type=cache,target="${GOCACHE}" \
  mkdir /dest \
  # clone 0KN opt-specific katzenpost branch, if one does not exist
  && if [ ! -d "${KATZENPOST_DIR}" ]; then make katzenpost_dir=${KATZENPOST_DIR} -C /src/docker clone-katzenpost ; fi \
  # a function to build and move the binary
  && build() { cd ${KATZENPOST_DIR}/$1 ; b=$(basename $1) ; go build ${GO_BUILD_OPTS} ; chmod u+x $b ; mv $b /dest/$2; } \
  # clients
  && build authority/cmd/fetch fetch \
  && build ping ping \
  # server
  && build server/cmd/server server \
  ## servicenode plugins
  && build http/proxy/client proxy_client \
  && build http/proxy/server proxy_server \
  && build memspool/server/cmd/memspool memspool \
  && build pigeonhole/server/cmd/pigeonhole pigeonhole \
  && build panda/server/cmd/panda_server panda_server \
  && build server_plugins/cbor_plugins/echo-go echo_server

# Build 0KN mix network components
RUN --mount=type=cache,target="${GOCACHE}" \
  # a function to build and move the binary
  build() { cd /src/$1 ; b=$(basename $1) ; go build ${GO_BUILD_OPTS} ; chmod u+x $b ; mv $b /dest/$2; } \
  # pki
  && build pki pki \
  # genconfig
  && build genconfig/cmd/genconfig genconfig \
  # servicenode plugins
  && build server_plugins/cbor_plugins/http_proxy/cmd/http_proxy http_proxy \
  # clients
  && build apps/walletshield walletshield


FROM base AS node

ARG DIR_BIN=/opt/zkn

COPY --from=builder /dest ${DIR_BIN}

CMD ["/bin/bash"]
