package main

import (
	"fmt"
	"log"
	"sync"

	"github.com/ZeroKnowledgeNetwork/appchain-agent/clients/go/chainbridge"
	"github.com/fxamacker/cbor/v2"
)

// This is an example appchain agent client that uses the chainbridge go package
// to interact with the appchain and perform some tests.

var chBridge *chainbridge.ChainBridge

// Sends a command to the chainbridge and logs the response or any errors.
func sendCommand(command string, payload []byte) {
	response, err := chBridge.Command(command, payload)
	if err != nil {
		log.Printf("Error: %v", err)
	} else {
		log.Printf("Response (%s): %+v\n", command, response)
	}
}

func networkRegistration() {
	network1 := chainbridge.Network{
		Identifier: "0x000",
		Parameters: []byte("pA: 1\npB: 2\npC: 3\n"),
	}
	log.Printf("Network in: %+v", network1)

	// register the network
	command := fmt.Sprintf(
		chainbridge.Cmd_networks_register,
		network1.Identifier,
	)
	response, err := chBridge.Command(command, network1.Parameters)
	log.Printf("Response (%s): %+v\n", command, response)
	if err != nil {
		log.Printf("ChainBridge command error: %v", err)
	}
	if response.Error != "" {
		log.Printf("ChainBridge response error: %v", response.Error)
	}

	// retrieve the network
	command = fmt.Sprintf(chainbridge.Cmd_networks_getNetwork, network1.Identifier)
	response, err = chBridge.Command(command, nil)
	if err != nil {
		log.Printf("ChainBridge command error: %v", err)
	} else {
		log.Printf("Response (%s): %+v\n", command, response)
	}
	var network2 chainbridge.Network
	if err := chBridge.DataUnmarshal(response, &network2); err != nil {
		log.Printf("ChainBridge data error: %v", err)
	}
	log.Printf("Network out: %+v", network2)
}

func nodeRegistration() {
	node1 := chainbridge.Node{
		Identifier:    "node-5000",
		IsGatewayNode: false,
		IsServiceNode: false,
		IdentityKey:   []byte("0x1234"),
	}
	log.Printf("Node in: %+v", node1)

	// register the node
	command := fmt.Sprintf(
		chainbridge.Cmd_nodes_register,
		node1.Identifier,
		chainbridge.Bool2int(node1.IsGatewayNode),
		chainbridge.Bool2int(node1.IsServiceNode))
	response, err := chBridge.Command(command, node1.IdentityKey)
	log.Printf("Response (%s): %+v\n", command, response)
	if err != nil {
		log.Printf("ChainBridge command error: %v", err)
	}
	if response.Error != "" {
		log.Printf("ChainBridge response error: %v", response.Error)
	}

	// retrieve the node
	command = fmt.Sprintf(chainbridge.Cmd_nodes_getNode, node1.Identifier)
	response, err = chBridge.Command(command, nil)
	if err != nil {
		log.Printf("ChainBridge command error: %v", err)
	} else {
		log.Printf("Response (%s): %+v\n", command, response)
	}
	var node2 chainbridge.Node
	if err := chBridge.DataUnmarshal(response, &node2); err != nil {
		log.Printf("ChainBridge data error: %v", err)
	}
	log.Printf("Node out: %+v", node2)
}

func pkiMixDescriptor() {
	// arbitrary struct to test round-trip of CBOR + IPFS binary data
	type TestData struct {
		Status string `cbor:"status"`
		Data   string `cbor:"data"`
		A      int    `cbor:"a,omitempty"`
		B      string `cbor:"b,omitempty"`
	}

	td := TestData{
		Status: "ok",
		Data:   "yo!",
		A:      101,
		B:      "0x8888",
	}

	log.Printf("Data in: %+v", td)
	enc, err := cbor.Marshal(td)
	if err != nil {
		log.Printf("CBOR Error: %v", err)
	}

	epoch := 1000
	id := "node-5000"

	// send encoded data as payload to IPFS and the appchain
	command := fmt.Sprintf(chainbridge.Cmd_pki_setMixDescriptor, epoch, id)
	response, err := chBridge.Command(command, enc)
	log.Printf("Response (%s): %+v\n", command, response)
	if err != nil {
		log.Printf("ChainBridge command error: %v", err)
	}
	if response.Error != "" {
		log.Printf("ChainBridge response error: %v", response.Error)
	}

	// retrieve the stored data
	command = fmt.Sprintf(chainbridge.Cmd_pki_getMixDescriptor, epoch, id)
	response, err = chBridge.Command(command, nil)
	if err != nil {
		log.Printf("ChainBridge command error: %v", err)
	} else {
		log.Printf("Response (%s): %+v\n", command, response)
	}
	data, err := chBridge.GetDataBytes(response)
	if err != nil {
		log.Printf("ChainBridge data error: %v", err)
	}

	// decode the CBOR data
	var td2 TestData
	err = cbor.Unmarshal(data, &td2)
	if err != nil {
		log.Printf("CBOR Error: %v", err)
	}
	log.Printf("Data out: %+v", td2)
}

func getGenesisEpoch() uint64 {
	response, err := chBridge.Command(chainbridge.Cmd_pki_getGenesisEpoch, nil)
	if err != nil {
		log.Printf("ChainBridge command error: %v", err)
	}

	genesisEpoch, err := chBridge.GetDataUInt(response)
	if err == chainbridge.ErrNoData {
		genesisEpoch = 0 // default
	} else if err != nil {
		log.Printf("ChainBridge data error: %v", err)
	}

	return genesisEpoch
}

func main() {
	// Either launch the agent with the command and its arguments
	// ?: chBridge := chainbridge.NewChainBridge(
	// ?: 	"pnpm", "run", "agent",
	// ?: 	"--admin",
	// ?: 	"--key", "/tmp/admin.key",
	// ?: 	"--listen",
	// ?: 	"--socket-format", "cbor",
	// ?: 	"--ipfs",
	// ?: )
	// Or simply connect to the existing socket file
	chBridge = chainbridge.NewChainBridge("/tmp/appchain.sock")

	// Optionally set an error handler for errors not returned by functions
	chBridge.SetErrorHandler(func(err error) {
		log.Printf("Error: %v", err)
	})

	chBridge.SetLogHandler(func(message string) {
		log.Printf("chainbridge: %s", message)
	})

	if err := chBridge.Start(); err != nil {
		log.Fatal(err)
	}

	defer chBridge.Stop()

	sendCommand("admin getAdmin", nil)
	sendCommand("admin setAdmin", nil)
	sendCommand("admin getAdmin", nil)
	sendCommand("nodes setRegistrationStake 0", nil)
	sendCommand("nodes openRegistration", nil)
	sendCommand("token getBalance", nil)
	sendCommand("faucet getEnabled", nil)
	sendCommand("faucet setEnabled 1", nil)
	sendCommand("faucet getEnabled", nil)

	// excute many commands and wait for them all to complete
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sendCommand(fmt.Sprintf("unknown-command-%d", i), nil)
		}(i)
	}
	wg.Wait()

	networkRegistration()
	nodeRegistration()

	log.Printf("genesisEpoch: %d", getGenesisEpoch())
	pkiMixDescriptor()
	log.Printf("genesisEpoch: %d", getGenesisEpoch())
}
