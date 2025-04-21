warped?=false
ldflags="-buildid= -X github.com/katzenpost/katzenpost/core/epochtime.WarpedEpoch=${warped}"

.PHONY: all
all: cmd/pki/pki

cmd/pki/pki: server/*.go cmd/pki/*.go
	cd cmd/pki && go build -trimpath -ldflags ${ldflags}

.PHONY: clean
clean:
	rm -f cmd/pki/pki
