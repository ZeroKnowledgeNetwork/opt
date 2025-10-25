warped?=true
compression?=true

mixes=3
auths=3
gateways=1
serviceNodes=1

UserForwardPayloadLength=30000

distro=alpine
net_name=voting_mixnet
docker_compose_yml=$(net_name)/docker-compose.yml
sh=$(shell if echo ${distro}|grep -q alpine; then echo sh; else echo bash; fi)
cache_dir=cache
log_level=DEBUG
docker=$(shell if which podman|grep -q .; then echo podman; else echo docker; fi)
ldflags="-extldflags '-Wl,-z,stack-size=0x1F40000' -buildid= -X github.com/katzenpost/katzenpost/core/epochtime.WarpedEpoch=${warped} -X github.com/ZeroKnowledgeNetwork/opt/common.CompressionEnabled=${compression}"
uid?=$(shell [ "$$SUDO_UID" != "" ] && echo "$$SUDO_UID" || id -u)
gid?=$(shell [ "$$SUDO_GID" != "" ] && echo "$$SUDO_GID" || id -g)
docker_user?=$(shell if echo ${docker}|grep -q podman; then echo 0:0; else echo ${uid}:${gid}; fi)
docker_args=--user ${docker_user} --volume $(shell readlink -f $(katzenpost_dir)):/go/katzenpost --workdir /go/katzenpost -v $(shell readlink -f $(katzenpost_dir))/docker/$(cache_dir)/go:/go/ -e GOCACHE=/go/cache
mount_net_name=-v $(katzenpost_dir)/docker/$(net_name):/$(net_name)
mount_opt=-v $(shell readlink -f ..):/go/opt
docker_run_sh=$(docker) run ${docker_args} $(mount_net_name) $(mount_opt) --rm katzenpost-$(distro)_base $(sh) -c

katzenpost_dir?=/tmp/katzenpost.opt
katzenpost_version?=$(shell grep -E '^	github.com/katzenpost/katzenpost ' ../go.mod | awk '{print $$2}')
net_dir=$(katzenpost_dir)/docker/$(net_name)

# export variables to the environment for consumption by invoked Makefile(s)
export

define SERVICENODE_PLUGINS
  [[ServiceNode.CBORPluginKaetzchen]]
    Capability = "http_proxy"
    Endpoint = "http_proxy"
    Command = "/$(net_name)/http_proxy.$(distro)"
    MaxConcurrency = 1
    Disable = false
    [ServiceNode.CBORPluginKaetzchen.Config]
      config = "/$(net_name)/servicenode1/http_proxy_config.toml"
      log_dir = "/$(net_name)/servicenode1"
endef

.PHONY: help
help:
	@echo
	@echo "These make targets allow you to control custom apps:"
	@echo " walletshield-start - start walletshield client"
	@echo " walletshield-logs  - show walletshield logs"
	@echo " walletshield-stop  - stop walletshield client"
	@echo " walletshield-probe - run walletshield test probe"
	@$(MAKE) -e -C $(katzenpost_dir)/docker $@

.PHONY: custom-binaries
custom-binaries: $(net_dir)/http_proxy.$(distro)

.PHONY: custom-config
custom-config:
	printf '%s\n' "$$SERVICENODE_PLUGINS" | sed -i '/^\[ServiceNode\]/r /dev/stdin' $(net_dir)/servicenode1/katzenpost.toml

.PHONY: clone-katzenpost
clone-katzenpost:
	if [ ! -d "$(katzenpost_dir)" ]; then \
		git clone \
			--branch $(katzenpost_version) \
			--depth 1 \
			--single-branch \
			https://github.com/katzenpost/katzenpost.git \
			$(katzenpost_dir); \
	fi

$(net_dir)/http_proxy.$(distro): $(katzenpost_dir)/docker/$(distro)_base.stamp | $(net_name) $(cache_dir)
	$(docker_run_sh) 'cd /go/opt/server_plugins/cbor_plugins/http_proxy/cmd/http_proxy ; go build -trimpath -ldflags ${ldflags} && mv http_proxy /$(net_name)/http_proxy.$(distro)'
	cp ../server_plugins/cbor_plugins/http_proxy/http_proxy_config.toml $(net_dir)/servicenode1/

$(net_dir)/walletshield.$(distro): $(katzenpost_dir)/docker/$(distro)_base.stamp | $(net_name) $(cache_dir)
	$(docker_run_sh) 'cd /go/opt/apps/walletshield ; go build -trimpath -ldflags ${ldflags} && mv walletshield /$(net_name)/walletshield.$(distro)'

.PHONY: walletshield-probe
walletshield-probe: $(net_dir)/walletshield.$(distro) $(net_dir)/running.stamp | $(cache_dir)
	$(docker) run --network=host $(docker_args) $(mount_net_name) --rm katzenpost-$(distro)_base \
		/$(net_name)/walletshield.$(distro) -config /$(net_name)/client2/client.toml -log_level DEBUG -probe --probe_count 3

.PHONY: walletshield-start
walletshield-start: $(net_dir)/walletshield.$(distro) $(net_dir)/running.stamp | $(cache_dir)
	$(docker) run -d --network=host $(docker_args) $(mount_net_name) --name walletshield katzenpost-$(distro)_base \
		/$(net_name)/walletshield.$(distro) -config /$(net_name)/client2/client.toml -listen :7070 -log_level DEBUG

.PHONY: walletshield-logs
walletshield-logs:
	$(docker) logs --tail=100 walletshield

.PHONY: walletshield-stop
walletshield-stop:
	-$(docker) stop walletshield && $(docker) rm walletshield

.PHONY: clean
clean: walletshield-stop
	$(MAKE) -e -C $(katzenpost_dir)/docker $@
	rm -rf $(katzenpost_dir)

# start the testnet in the background with the local customizations
start: clone-katzenpost $(docker_compose_yml) custom-config custom-binaries
	$(MAKE) -e -C $(katzenpost_dir)/docker $@

# pass through all other targets to katzenpost/docker/Makefile
%: clone-katzenpost
	@echo ">> Passing '$@' to $(katzenpost_dir)/docker/Makefile"
	$(MAKE) -e -C $(katzenpost_dir)/docker $@
