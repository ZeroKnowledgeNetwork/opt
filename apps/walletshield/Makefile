warped?=true
ldflags="-X github.com/katzenpost/katzenpost/core/epochtime.WarpedEpoch=${warped}"

.PHONY: all
all:
	go build -trimpath -ldflags ${ldflags}

.PHONY: clean
clean:
	rm -f walletshield
