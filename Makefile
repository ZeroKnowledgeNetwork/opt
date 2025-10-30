compression?=true
warped?=true
ldflags="-X github.com/katzenpost/katzenpost/core/epochtime.WarpedEpoch=${warped} -X github.com/ZeroKnowledgeNetwork/opt/common.CompressionEnabled=${compression}"

.PHONY: all app-walletshield genconfig http_proxy pki clean

all: app-walletshield http_proxy genconfig pki

app-walletshield:
	cd apps/walletshield && go build -trimpath -ldflags ${ldflags}

genconfig:
	cd genconfig/cmd/genconfig && go build

http_proxy:
	cd server_plugins/cbor_plugins/http_proxy/cmd/http_proxy && go build -trimpath -ldflags ${ldflags}

pki:
	make -C pki

clean:
	make -C pki clean
	rm -f \
		apps/walletshield/walletshield \
		genconfig/cmd/genconfig/genconfig \
		server_plugins/cbor_plugins/http_proxy/cmd/http_proxy/http_proxy
