# Dockerized Test Networks

This directory provides Makefiles and scripts to set up a local, offline test network for developing
and testing ZKN mix network applications and server-side plugins. The setup leverages a
Podman-compatible `docker-compose` configuration for simulating a Katzenpost network environment.

The goal is to support core development workflows by enabling local testing of both client and
server mix network components in isolated, controlled conditions.

There are two Makefiles available, each corresponding to a different PKI.

- **`Makefile`:** (Default) Manages a local test network using Katzenpost’s voting PKI.
- **`Makefile.appchain`:** Uses ZKN’s ZKAppChain PKI.

## Voting PKI

This setup, managed by the default `Makefile`, covers ZKN-specifics and proxies other targets to
Katzenpost's `docker/Makefile`. For additional details, refer to the [Katzenpost Docker Test
Network documentation](https://github.com/katzenpost/katzenpost/tree/main/docker). The voting PKI
functionality offers less complex local testing of ZKN mix plugins and client apps that do not
require the appchain.

## Appchain PKI

This Makefile builds and manages a network of dockerized nodes from
[`node/Dockerfile`](./node/Dockerfile). It uses the [genconfig](../genconfig/) utility to create
configurations for nodes from the network info in [network.yml](./network.yml) using the
appchain-powered [pki](../pki/). Node interactions with the appchain are managed through the
appchain-agent, utilizing UNIX domain sockets for communication.

### Prerequisites

To run the Appchain PKI network, ensure the following components are available:

- [appchain-agent](https://github.com/ZeroKnowledgeNetwork/appchain-agent) Docker image
- An operational ZKN ZKAppChain

### Example Workflow

```bash
# with appchain-agent docker image built, and local appchain running

# a lil helper to decrease verbosity
alias makea='net=/tmp/mix make -f Makefile.appchain'

# register and activate a network with the local appchain
makea init

# build the docker image, configure, start the network, wait for the epoch, then probe
makea start wait probe

# stop the network and clean up
makea clean

# build the docker image and configure (without starting network)
# to inspect or manually edit the configuration files before continuing
makea config

# start the network without rebuilding or reconfiguring, wait for the epoch
makea _start wait

# test the network with a client sending 10 test probes
makea probe_count=10 probe

# watch log files
tail -f /tmp/appchain-mixnet/*/*.log

# stop the network (without cleaning up)
makea stop

# clean up (remove config files)
makea clean
```
