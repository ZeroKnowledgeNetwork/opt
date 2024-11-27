#!/bin/bash

set -euo pipefail

network_id=${1:-_} # default to active network

# Load configuration from the .env file in the same directory as the script
dir_script=$(dirname "$(readlink -f "$0")")
file_env="${dir_script}/.env"
[ ! -f "${file_env}" ] && echo "Error: .env file not found in ${dir_script}" && exit 1
echo "Loading environment variables from ${file_env}"
export $(grep -v '^#' "${file_env}" | xargs)

# Verify that required environment variables are set
for var in \
  DIR_BASE \
  DIR_BIN \
  IMAGE_AGENT \
  IMAGE_NODE \
  NODE_ADDRESS \
  NODE_ID \
  NODE_LOG_LEVEL \
  NODE_METRICS \
  NODE_PORT \
  NODE_TYPE \
  USE_LOCAL_IMAGES \
  VOLUME_MIXNET \
; do
  : "${!var:?Environment variable ${var} is required}"
done

# Configuration
file_network="${VOLUME_MIXNET}/network.yml"

# Determine container tool and related settings
if command -v podman >/dev/null; then
  docker="podman"
  docker_user=$(if podman info --format '{{.Host.Security.Rootless}}' | grep -iq "true"; then echo "$(id -u):$(id -g)"; else echo "0:0"; fi)
  docker_compose="DOCKER_USER=${docker_user} DOCKER_HOST=unix://${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/podman/podman.sock docker compose"
else
  docker="docker"
  docker_user="${SUDO_UID:-$(id -u)}:${SUDO_GID:-$(id -g)}"
  docker_compose="DOCKER_USER=${docker_user} docker compose"
fi
docker_run="${docker} run \
  --network=host \
  --rm \
  --user ${docker_user} \
  --volume ${VOLUME_MIXNET}:${DIR_BASE}"

# Pull required Docker images
[ ${USE_LOCAL_IMAGES} -eq "0" ] && echo -e "\nPulling required Docker images..." && eval ${docker_compose} pull

# Fetch network info
echo -e "\nFetching active network configuration..."
eval ${docker_run} \
  ${IMAGE_AGENT} \
  pnpm run agent \
    --ipfs \
    --ipfs-data ${DIR_BASE}/ipfs \
    networks getNetwork ${network_id} ${DIR_BASE}/network.yml
[ ! -f "${file_network}" ] && echo "Error: Network configuration file not generated." && exit 1
echo "Network configuration saved to ${file_network}"

# Generate node configuration
echo -e "\nGenerating node configuration..."
eval ${docker_run} \
  ${IMAGE_NODE} \
  ${DIR_BIN}/genconfig \
  -input ${DIR_BASE}/network.yml \
  -binary-prefix ${DIR_BIN}/ \
  -dir-base ${DIR_BASE} \
  -dir-out ${DIR_BASE} \
  -address ${NODE_ADDRESS} \
  -identifier ${NODE_ID} \
  -log-level ${NODE_LOG_LEVEL} \
  -metrics ${NODE_METRICS} \
  -port ${NODE_PORT} \
  -type ${NODE_TYPE}
echo "Node configuration generated successfully."

# Start services
echo -e "\nStarting services..."
eval ${docker_compose} up -d

echo -e "\nNode setup and services started successfully."
