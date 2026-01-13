package chainbridge

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/lesismal/nbio"
)

type ChainBridge struct {
	cmd          *exec.Cmd
	socketFile   string
	client       *nbio.Engine
	conn         *nbio.Conn
	responses    sync.Map
	idCounter    int
	errorHandler func(error)
	logHandler   func(string)
	dialRetries  int
	dialTimeout  time.Duration
	cmdTimeout   time.Duration
	reconnect    bool
	isConnected  bool

	idCounterMu   sync.Mutex
	isConnectedMu sync.Mutex
	reconnectMu   sync.Mutex
}

type CommandRequest struct {
	Command string `cbor:"command"`
	Payload []byte `cbor:"payload,omitempty"`
	ID      int    `cbor:"id,omitempty"`
}

type CommandResponse struct {
	Status string      `cbor:"status"`
	Data   interface{} `cbor:"data,omitempty"`
	Error  string      `cbor:"error,omitempty"`
	ID     int         `cbor:"id,omitempty"`
	TX     string      `cbor:"tx,omitempty"`
}

type Network struct {
	Identifier string `cbor:"identifier"`
	Parameters []byte `cbor:"parameters"`
}

type Node struct {
	Administrator string `cbor:"administrator"`
	Identifier    string `cbor:"identifier"`
	IsGatewayNode bool   `cbor:"isGatewayNode"`
	IsServiceNode bool   `cbor:"isServiceNode"`
	IdentityKey   []byte `cbor:"identityKey"`
}

var (
	// ErrNoData is the error returned when attempting to convert data from a command response with no data
	// Use this to test if requested appchain state is undefined
	ErrNoData = errors.New("ChainBridge: no data")

	// command response errors returned by the appchain
	Err_networks_alreadyRegistered = "Network already registered"
	Err_nodes_alreadyRegistered    = "Node already registered"
	// ?: Err_nodes_nodeNotValid            = "Node attributes are not valid"
	// ?: Err_nodes_registrationNotOpened   = "Node registration is not opened"
	// ?: Err_nodes_registrationStakeNotSet = "Node registration stake is not set"
	// ?: Err_nodes_senderNotNodeAdmin      = "Sender is not the node administrator"

	// TODO: consider this may evolve or go away... with improved schedule
	// ?: Err_pki_documentExists        = "Document already exists for the epoch"

	// ?: Err_pki_lateDescriptor        = "Mix descriptor is late for the epoch"
	// ?: Err_pki_mixDescriptorExists   = "Mix descriptor exists for the epoch and identifier"
	// ?: Err_pki_mixDescriptorNotValid = "Mix descriptor is not valid"
	// ?: Err_pki_noDescriptors         = "No descriptors for the epoch"
)

// appchain-agent commands; cuz compile errors are better than runtime errors
var (
	Cmd_consensus_commitVote        = "consensus commitVote %d %d %d"
	Cmd_consensus_getConsensus      = "consensus getConsensus %d %d"
	Cmd_consensus_register          = "consensus register"
	Cmd_consensus_revealVote        = "consensus revealVote %d %d %d"
	Cmd_networks_getNetwork         = "networks getNetwork %s"
	Cmd_networks_register           = "networks register %s"
	Cmd_nodes_getNode               = "nodes getNode %s"
	Cmd_nodes_register              = "nodes register %s %d %d"
	Cmd_pki_getGenesisEpoch         = "pki getGenesisEpoch"
	Cmd_pki_getMixDescriptor        = "pki getMixDescriptor %d %s"
	Cmd_pki_getMixDescriptorByIndex = "pki getMixDescriptorByIndex %d %d"
	Cmd_pki_getMixDescriptorCounter = "pki getMixDescriptorCounter %d"
	Cmd_pki_setMixDescriptor        = "pki setMixDescriptor %d %s"
)

// Initialize a ChainBridge instance. Accepts either:
// - a socket path or
// - a command with its arguments to launch the process.
func NewChainBridge(socketFileOrCommandName string, commandArgs ...string) *ChainBridge {
	var cmd *exec.Cmd
	if len(commandArgs) > 0 {
		cmd = exec.Command(socketFileOrCommandName, commandArgs...)
	}

	// TODO pass some of these as arguments
	// TODO cmdTimeout should correspond to appchain-agent txStatusInterval x txStatusRetries
	return &ChainBridge{
		cmd:         cmd,
		socketFile:  socketFileOrCommandName,
		idCounter:   0,
		dialRetries: 15,
		dialTimeout: 10 * time.Second,
		cmdTimeout:  50 * time.Second,
		reconnect:   true,
		isConnected: false,
	}
}

// Set a custom error handler to be called when an error occurs.
func (c *ChainBridge) SetErrorHandler(handler func(error)) {
	c.errorHandler = handler
}

// Set a custom log handler to be called for non-error logs.
func (c *ChainBridge) SetLogHandler(handler func(string)) {
	c.logHandler = handler
}

func (c *ChainBridge) handleError(err error) {
	if c.errorHandler != nil {
		c.errorHandler(err)
	}
}

func (c *ChainBridge) log(message string) {
	if c.logHandler != nil {
		c.logHandler(message)
	}
}

func (c *ChainBridge) connectToSocket() error {
	for i := 0; i < c.dialRetries; i++ {
		c.log(fmt.Sprintf("Attempting to connect to socket: %s (attempt %d/%d)", c.socketFile, i+1, c.dialRetries))
		conn, err := nbio.DialTimeout("unix", c.socketFile, c.dialTimeout)
		if err == nil {
			c.conn, err = c.client.AddConn(conn)
			if err == nil {
				c.log("Successfully connected to socket.")
				c.isConnectedMu.Lock()
				c.isConnected = true
				c.isConnectedMu.Unlock()
				return nil
			}
		}
		if !c.reconnect {
			return nil
		}
		c.log(fmt.Sprintf("Failed to connect to socket: %v", err))
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("Failed to connect after %d attempts", c.dialRetries)
}

// Start the ChainBridge either by:
// - connecting to the existing socket path or
// - executing the provided command, then connecting to the socket path printed in its stdout.
func (c *ChainBridge) Start() error {
	c.log("Starting...")

	if c.cmd != nil {
		stdout, err := c.cmd.StdoutPipe()
		if err != nil {
			return err
		}
		stderr, err := c.cmd.StderrPipe()
		if err != nil {
			return err
		}

		if err := c.cmd.Start(); err != nil {
			return err
		}

		// Read the socket location from stdout
		outScanner := bufio.NewScanner(io.MultiReader(stdout, stderr))
		for outScanner.Scan() {
			line := outScanner.Text()
			const prefix = "UNIX_SOCKET_PATH="
			if strings.HasPrefix(line, prefix) {
				c.socketFile = strings.TrimPrefix(line, prefix)
				break
			}
		}
		if err := outScanner.Err(); err != nil {
			return err
		}

		if c.socketFile == "" {
			return fmt.Errorf("socket path not found in output")
		}
	}

	c.client = nbio.NewEngine(nbio.Config{})
	c.client.OnData(c.onData)
	c.client.OnClose(c.onClose)

	if err := c.client.Start(); err != nil {
		return fmt.Errorf("nbio client start failed: %v", err)
	}

	c.reconnectMu.Lock()
	c.reconnect = true
	c.reconnectMu.Unlock()

	return c.connectToSocket()
}

func (c *ChainBridge) onData(conn *nbio.Conn, data []byte) {
	var response CommandResponse
	if err := cbor.Unmarshal(data, &response); err != nil {
		c.handleError(fmt.Errorf("CBOR Unmarshal error: %w", err))
		return
	}

	// Dispatch the response to the correct channel
	if ch, ok := c.responses.Load(response.ID); ok {
		ch.(chan CommandResponse) <- response
		c.responses.Delete(response.ID)
	}
}

func (c *ChainBridge) onClose(conn *nbio.Conn, err error) {
	c.isConnectedMu.Lock()
	c.isConnected = false
	c.isConnectedMu.Unlock()

	c.log("Connection closed")

	if c.reconnect {
		if err := c.connectToSocket(); err != nil {
			c.handleError(fmt.Errorf("Failed to reconnect: %w", err))
		}
	}
}

func (c *ChainBridge) Stop() error {
	c.log("Stopping...")

	c.reconnectMu.Lock()
	c.reconnect = false
	c.reconnectMu.Unlock()

	if c.client != nil {
		c.client.Stop()
	}

	if c.cmd != nil {
		if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return err
		}
		// the agent process should cleanup, but make sure
		os.Remove(c.socketFile)
	}

	return nil
}

func (c *ChainBridge) Command(command string, payload []byte) (CommandResponse, error) {
	var response CommandResponse

	if !c.isConnected {
		return response, fmt.Errorf("Not connected")
	}

	// Generate a unique ID for the request
	c.idCounterMu.Lock()
	c.idCounter++
	reqID := c.idCounter
	c.idCounterMu.Unlock()

	req := CommandRequest{
		Command: command,
		Payload: payload,
		ID:      reqID,
	}

	reqData, err := cbor.Marshal(req)
	if err != nil {
		return response, fmt.Errorf("CBOR Marshal error: %w", err)
	}

	// Create a response channel and store it in the map
	responseChan := make(chan CommandResponse, 1)
	c.responses.Store(req.ID, responseChan)

	// Send the request
	_, err = c.conn.Write(reqData)
	if err != nil {
		return response, fmt.Errorf("Write error: %w", err)
	}

	// Wait for the response with a timeout
	select {
	case response = <-responseChan:
		return response, nil
	case <-time.After(c.cmdTimeout):
		c.responses.Delete(req.ID)
		return response, fmt.Errorf("Timeout waiting for response to request ID=%d: %s", req.ID, req.Command)
	}
}

func (c *ChainBridge) GetDataBool(response CommandResponse) (bool, error) {
	if response.Data == nil {
		return false, ErrNoData
	}

	b, ok := response.Data.(bool)
	if !ok {
		return false, fmt.Errorf("unexpected data type: %T, expected bool", response.Data)
	}

	return b, nil
}

func (c *ChainBridge) GetDataBytes(response CommandResponse) ([]byte, error) {
	if response.Data == nil {
		return nil, ErrNoData
	}

	// Check if the data is a CBOR tag
	tag, ok := response.Data.(cbor.Tag)
	if !ok {
		return nil, fmt.Errorf("unexpected data type: %T, expected cbor.Tag", response.Data)
	}

	// Ensure the content is of type []byte
	bytes, ok := tag.Content.([]byte)
	if !ok {
		return nil, fmt.Errorf("unexpected tag content type: %T, number: %d, expected []byte", tag.Content, tag.Number)
	}

	return bytes, nil
}

func (c *ChainBridge) GetDataUInt(response CommandResponse) (uint64, error) {
	if response.Data == nil {
		return 0, ErrNoData
	}

	str, ok := response.Data.(string)
	if !ok {
		return 0, fmt.Errorf("unexpected data type: %T, expected string", response.Data)
	}

	num, err := strconv.ParseUint(str, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse uint64: %w", err)
	}

	return num, nil
}

func (c *ChainBridge) DataUnmarshal(response CommandResponse, v interface{}) error {
	if response.Data == nil {
		return ErrNoData
	}

	bytes, ok := response.Data.([]byte)
	if !ok {
		return fmt.Errorf("unexpected data type: %T, expected []byte", response.Data)
	}

	err := cbor.Unmarshal(bytes, v)
	if err != nil {
		return fmt.Errorf("CBOR Error: %v", err)
	}

	return nil
}

func Bool2int(b bool) int {
	var i int
	if b {
		i = 1
	} else {
		i = 0
	}
	return i
}
