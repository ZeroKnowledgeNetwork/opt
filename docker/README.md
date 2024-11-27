# Dockerized Test Networks

This directory provides Makefiles and scripts to set up a local, offline test network for developing
and testing 0KN mix network applications and server-side plugins. The setup leverages a
Podman-compatible `docker-compose` configuration for simulating a Katzenpost network environment.

The goal is to support core development workflows by enabling local testing of both client and
server mix network components in isolated, controlled conditions.

There are two Makefiles available, each corresponding to a different PKI.

- **`Makefile`:** (Default) Manages a local test network using Katzenpost’s voting PKI.
- **`Makefile.appchain`:** Uses 0KN’s ZKAppChain PKI.

## Voting PKI

This setup, managed by the default `Makefile`, covers 0KN-specifics and proxies other targets to
Katzenpost's `docker/Makefile`. For additional details, refer to the [Katzenpost Docker Test
Network documentation](https://github.com/katzenpost/katzenpost/tree/main/docker). The voting PKI
functionality offers less complex local testing of 0KN mix plugins and client apps that do not
require the appchain.

## Appchain PKI

This Makefile builds and manages a network of dockerized nodes from
[`node/Dockerfile`](./node/Dockerfile). It uses the [genconfig](../genconfig/) utility to create
configurations for nodes from the network info in [network.yml](./network.yml) using the
appchain-powered [pki](../pki/). Node interactions with the appchain are managed through the
appchain-agent, utilizing UNIX domain sockets for communication.

### Prerequisites

To run the Appchain PKI network, ensure the following components are available:

- [appchain-agent](https://github.com/0KnowledgeNetwork/appchain-agent) Docker image
- An operational 0KN ZKAppChain

### Example Workflow

```bash
# build the appchain-agent docker image
cd appchain-agent && make image

# start local appchain instance, then:

# register and activate a network with the local appchain
net=/tmp/appchain-mixnet make -f Makefile.appchain init

# build the docker image, configure, start the network, wait for the epoch, then probe
net=/tmp/appchain-mixnet make -f Makefile.appchain start wait probe

# stop the network and clean up
net=/tmp/appchain-mixnet make -f Makefile.appchain clean

# build the docker image and configure (without starting network)
# to inspect or manually edit the configuration files before continuing
net=/tmp/appchain-mixnet make -f Makefile.appchain config

# start the network without rebuilding or reconfiguring, wait for the epoch
net=/tmp/appchain-mixnet make -f Makefile.appchain _start wait

# test the network with a client sending 10 test probes
net=/tmp/appchain-mixnet probe_count=10 make -f Makefile.appchain probe

# watch log files
tail -f /tmp/appchain-mixnet/*/*.log

# stop the network (without cleaning up)
net=/tmp/appchain-mixnet make -f Makefile.appchain stop
```
