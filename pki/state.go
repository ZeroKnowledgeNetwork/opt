// related katzenpost:authority/voting/server/state.go

package main

import (
	"crypto/hmac"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gopkg.in/op/go-logging.v1"

	"github.com/fxamacker/cbor/v2"
	"github.com/katzenpost/hpqc/hash"
	"github.com/katzenpost/hpqc/rand"
	"github.com/katzenpost/hpqc/sign"
	signpem "github.com/katzenpost/hpqc/sign/pem"
	signSchemes "github.com/katzenpost/hpqc/sign/schemes"
	"github.com/katzenpost/katzenpost/core/epochtime"
	"github.com/katzenpost/katzenpost/core/pki"
	"github.com/katzenpost/katzenpost/core/sphinx/constants"
	"github.com/katzenpost/katzenpost/core/worker"

	"github.com/ZeroKnowledgeNetwork/appchain-agent/clients/go/chainbridge"
	"github.com/ZeroKnowledgeNetwork/opt/pki/config"
)

const (
	stateBootstrap        = "bootstrap"
	stateDescriptorSend   = "descriptor_send"
	stateAcceptDescriptor = "accept_desc"
	stateAcceptVote       = "accept_vote"
	stateConfirmConsensus = "confirm_consensus"

	publicKeyHashSize = 32
)

// NOTE: 2024-11-01:
// Parts of katzenpost use MixPublishDeadline and PublishConsensusDeadline defined in
// katzenpost:authority/voting/server/state.go
// So, we preserve that aspect of the epoch schedule.
var (
	MixPublishDeadline       = epochtime.Period * 1 / 8 // Do NOT change this
	DescriptorBlockDeadline  = epochtime.Period * 2 / 8
	AuthorityVoteDeadline    = epochtime.Period * 3 / 8
	PublishConsensusDeadline = epochtime.Period * 5 / 8 // Do NOT change this
	DocGenerationDeadline    = epochtime.Period * 7 / 8
	RandomCourtessyDelay     = epochtime.Period * 1 / 16 // duration to distribute load across synchronized nodes
	errGone                  = errors.New("authority: Requested epoch will never get a Document")
	errNotYet                = errors.New("authority: Document is not ready yet")
	errInvalidTopology       = errors.New("authority: Invalid Topology")
)

type state struct {
	sync.RWMutex
	worker.Worker

	s           *Server
	log         *logging.Logger
	chainBridge *chainbridge.ChainBridge
	ccbor       cbor.EncMode // a la katzenpost:core/pki/document.go

	// locally registered node, only one allowed
	// mix descriptor uploads to this authority are restricted to this node
	authorizedNode *chainbridge.Node

	documents   map[uint64]*pki.Document
	descriptors map[uint64]map[[publicKeyHashSize]byte]*pki.MixDescriptor

	votingEpoch  uint64
	genesisEpoch uint64
	state        string
}

func (s *state) Halt() {
	s.Worker.Halt()
}

func (s *state) worker() {
	for {
		select {
		case <-s.HaltCh():
			s.log.Debugf("authority: Terminating gracefully.")
			return
		case <-s.fsm():
			s.log.Debugf("authority: Wakeup due to voting schedule.")
		}
	}
}

// Returns a random delay to distribute load across synchronized nodes
func (s *state) courtessyDelay() time.Duration {
	return time.Duration(rand.NewMath().Float64() * float64(RandomCourtessyDelay))
}

func (s *state) fsm() <-chan time.Time {
	s.Lock()
	var sleep time.Duration
	epoch, elapsed, nextEpoch := epochtime.Now()
	s.log.Debugf("Current epoch %d, remaining time: %s, state: %s", epoch, nextEpoch, s.state)

	switch s.state {
	case stateBootstrap:
		// TODO: ensure network is ready and locally registered node is eligible for participation
		s.genesisEpoch = 0
		s.backgroundFetchConsensus(epoch - 1)
		s.backgroundFetchConsensus(epoch)
		if elapsed > MixPublishDeadline {
			s.log.Errorf("Too late to vote this round, sleeping until %s", nextEpoch)
			sleep = nextEpoch
			s.votingEpoch = epoch + 2
			s.state = stateBootstrap
		} else {
			s.votingEpoch = epoch + 1
			s.state = stateDescriptorSend
			sleep = MixPublishDeadline - elapsed + s.courtessyDelay()
			if sleep < 0 {
				sleep = 0
			}
			s.log.Noticef("Bootstrapping for %d", s.votingEpoch)
		}
	case stateDescriptorSend:
		// Send mix descriptor to the appchain
		pk := hash.Sum256(s.authorizedNode.IdentityKey)
		desc, ok := s.descriptors[s.votingEpoch][pk]
		if ok {
			s.submitDescriptorToAppchain(desc, s.votingEpoch)
		} else {
			s.log.Errorf("❌ No descriptor for epoch %d", s.votingEpoch)
		}
		s.state = stateAcceptDescriptor
		sleep = DescriptorBlockDeadline - elapsed + s.courtessyDelay()
	case stateAcceptDescriptor:
		doc, err := s.getVote(s.votingEpoch)
		if err == nil {
			s.sendVoteToAppchain(doc, s.votingEpoch)
		} else {
			s.log.Errorf("❌ Failed to compute vote for epoch %v: %s", s.votingEpoch, err)
		}
		s.state = stateAcceptVote
		_, nowelapsed, _ := epochtime.Now()
		sleep = AuthorityVoteDeadline - nowelapsed
	case stateAcceptVote:
		s.backgroundFetchConsensus(s.votingEpoch)
		s.state = stateConfirmConsensus
		_, nowelapsed, _ := epochtime.Now()
		sleep = PublishConsensusDeadline - nowelapsed
	case stateConfirmConsensus:
		// See if consensus doc was retrieved from the appchain
		_, ok := s.documents[epoch+1]
		if ok {
			s.state = stateDescriptorSend
			sleep = MixPublishDeadline + nextEpoch + s.courtessyDelay()
			s.votingEpoch++
		} else {
			s.log.Error("No document for epoch %v", epoch+1)
			s.state = stateBootstrap
			s.votingEpoch = epoch + 2 // vote on epoch+2 in epoch+1
			sleep = nextEpoch
		}
	default:
	}
	s.pruneDocuments()
	s.log.Debugf("authority: FSM in state %v until %s", s.state, sleep)
	s.Unlock()
	return time.After(sleep)
}

// getVote produces a pki.Document using all MixDescriptors recorded with the appchain
func (s *state) getVote(epoch uint64) (*pki.Document, error) {
	// Is there a prior consensus? If so, obtain the GenesisEpoch
	if d, ok := s.documents[s.votingEpoch-1]; ok {
		s.log.Debugf("Restoring genesisEpoch %d from document cache", d.GenesisEpoch)
		s.genesisEpoch = d.GenesisEpoch
		d.PKISignatureScheme = s.s.cfg.Server.PKISignatureScheme
	} else {
		s.log.Debugf("Setting genesisEpoch %d from votingEpoch", s.votingEpoch)
		s.genesisEpoch = s.votingEpoch
	}

	descriptors, err := s.chPKIGetMixDescriptors(epoch)
	if err != nil {
		return nil, err
	}

	// vote topology is irrelevent.
	// TODO: use an appchain block hash as srv
	var zeros [32]byte
	doc := s.getDocument(descriptors, s.s.cfg.Parameters, zeros[:])

	// Note: For appchain-pki, upload unsigned document and sign it upon local save.
	// simulate SignDocument's setting of doc version, required by IsDocumentWellFormed
	doc.Version = pki.DocumentVersion

	if err := pki.IsDocumentWellFormed(doc, nil); err != nil {
		s.log.Errorf("pki: ❌ getVote: IsDocumentWellFormed: %s", err)
		return nil, err
	}

	return doc, nil
}

func (s *state) sendVoteToAppchain(doc *pki.Document, epoch uint64) {
	if err := s.chPKISetDocument(doc); err != nil {
		s.log.Errorf("❌ sendVoteToAppchain: Error setting document for epoch %d: %v", epoch, err)
	} else {
		s.log.Noticef("✅ sendVoteToAppchain: Set document for epoch %d", epoch)
	}
}

func (s *state) doSignDocument(signer sign.PrivateKey, verifier sign.PublicKey, d *pki.Document) ([]byte, error) {
	signAt := time.Now()
	sig, err := pki.SignDocument(signer, verifier, d)
	s.log.Noticef("pki.SignDocument took %v", time.Since(signAt))
	return sig, err
}

func (s *state) getDocument(descriptors []*pki.MixDescriptor, params *config.Parameters, srv []byte) *pki.Document {
	// Carve out the descriptors between providers and nodes.
	gateways := []*pki.MixDescriptor{}
	serviceNodes := []*pki.MixDescriptor{}
	nodes := []*pki.MixDescriptor{}

	for _, v := range descriptors {
		if v.IsGatewayNode {
			gateways = append(gateways, v)
		} else if v.IsServiceNode {
			serviceNodes = append(serviceNodes, v)
		} else {
			nodes = append(nodes, v)
		}
	}

	// Assign nodes to layers.
	var topology [][]*pki.MixDescriptor

	// We prefer to not randomize the topology if there is an existing topology to avoid
	// partitioning the client anonymity set when messages from an earlier epoch are
	// differentiable as such because of topology violations in the present epoch.
	if d, ok := s.documents[s.votingEpoch-1]; ok {
		topology = s.generateTopology(nodes, d, srv)
	} else {
		topology = s.generateRandomTopology(nodes, srv)
	}

	nodesPerLayer := len(nodes) / s.s.cfg.Debug.Layers
	lambdaG := computeLambdaGFromNodesPerLayer(s.s.cfg, nodesPerLayer)
	s.log.Debugf("computed lambdaG from %d nodes per layer is %f", nodesPerLayer, lambdaG)

	// Build the Document.
	doc := &pki.Document{
		Epoch:              s.votingEpoch,
		GenesisEpoch:       s.genesisEpoch,
		SendRatePerMinute:  params.SendRatePerMinute,
		Mu:                 params.Mu,
		MuMaxDelay:         params.MuMaxDelay,
		LambdaP:            params.LambdaP,
		LambdaPMaxDelay:    params.LambdaPMaxDelay,
		LambdaL:            params.LambdaL,
		LambdaLMaxDelay:    params.LambdaLMaxDelay,
		LambdaD:            params.LambdaD,
		LambdaDMaxDelay:    params.LambdaDMaxDelay,
		LambdaM:            params.LambdaM,
		LambdaMMaxDelay:    params.LambdaMMaxDelay,
		LambdaG:            lambdaG,
		LambdaGMaxDelay:    params.LambdaGMaxDelay,
		Topology:           topology,
		GatewayNodes:       gateways,
		ServiceNodes:       serviceNodes,
		SharedRandomValue:  srv,
		PriorSharedRandom:  [][]byte{srv}, // this is made up, only to suffice IsDocumentWellFormed
		SphinxGeometryHash: s.s.geo.Hash(),
		PKISignatureScheme: s.s.cfg.Server.PKISignatureScheme,
	}
	return doc
}

func (s *state) generateTopology(nodeList []*pki.MixDescriptor, doc *pki.Document, srv []byte) [][]*pki.MixDescriptor {
	s.log.Debugf("Generating mix topology.")

	nodeMap := make(map[[constants.NodeIDLength]byte]*pki.MixDescriptor)
	for _, v := range nodeList {
		id := hash.Sum256(v.IdentityKey)
		nodeMap[id] = v
	}

	// TODO: consider strategies for balancing topology? Should this happen automatically?
	//       the current strategy will rebalance by limiting the number of nodes that are
	//       (re)inserted at each layer and placing these nodes into another layer.

	// Since there is an existing network topology, use that as the basis for
	// generating the mix topology such that the number of nodes per layer is
	// approximately equal, and as many nodes as possible retain their existing
	// layer assignment to minimise network churn.
	// The srv is used, when available, to ensure the ordering of new nodes
	// is deterministic between authorities
	rng, err := rand.NewDeterministicRandReader(srv[:])
	if err != nil {
		s.log.Errorf("DeterministicRandReader() failed to initialize: %v", err)
		s.s.fatalErrCh <- err
	}
	targetNodesPerLayer := len(nodeList) / s.s.cfg.Debug.Layers
	topology := make([][]*pki.MixDescriptor, s.s.cfg.Debug.Layers)

	// Assign nodes that still exist up to the target size.
	for layer, nodes := range doc.Topology {
		nodeIndexes := rng.Perm(len(nodes))

		for _, idx := range nodeIndexes {
			if len(topology[layer]) >= targetNodesPerLayer {
				break
			}

			id := hash.Sum256(nodes[idx].IdentityKey)
			if n, ok := nodeMap[id]; ok {
				// There is a new descriptor with the same identity key,
				// as an existing descriptor in the previous document,
				// so preserve the layering.
				topology[layer] = append(topology[layer], n)
				delete(nodeMap, id)
			}
		}
	}

	// Flatten the map containing the nodes pending assignment.
	toAssign := make([]*pki.MixDescriptor, 0, len(nodeMap))
	for _, n := range nodeMap {
		toAssign = append(toAssign, n)
	}
	// must sort toAssign by ID!
	sortNodesByPublicKey(toAssign)

	assignIndexes := rng.Perm(len(toAssign))

	// Fill out any layers that are under the target size, by
	// randomly assigning from the pending list.
	idx := 0
	for layer := range doc.Topology {
		for len(topology[layer]) < targetNodesPerLayer {
			n := toAssign[assignIndexes[idx]]
			topology[layer] = append(topology[layer], n)
			idx++
		}
	}

	// Assign the remaining nodes.
	for layer := 0; idx < len(assignIndexes); idx++ {
		n := toAssign[assignIndexes[idx]]
		topology[layer] = append(topology[layer], n)
		layer++
		layer = layer % len(topology)
	}

	return topology
}

func (s *state) generateRandomTopology(nodes []*pki.MixDescriptor, srv []byte) [][]*pki.MixDescriptor {
	s.log.Debugf("Generating random mix topology.")

	// If there is no node history in the form of a previous consensus,
	// then the simplest thing to do is to randomly assign nodes to the
	// various layers.

	if len(srv) != 32 {
		err := errors.New("SharedRandomValue too short")
		s.log.Errorf("srv: %s", srv)
		s.s.fatalErrCh <- err
	}
	rng, err := rand.NewDeterministicRandReader(srv[:])
	if err != nil {
		s.log.Errorf("DeterministicRandReader() failed to initialize: %v", err)
		s.s.fatalErrCh <- err
	}

	nodeIndexes := rng.Perm(len(nodes))
	topology := make([][]*pki.MixDescriptor, s.s.cfg.Debug.Layers)
	for idx, layer := 0, 0; idx < len(nodes); idx++ {
		n := nodes[nodeIndexes[idx]]
		topology[layer] = append(topology[layer], n)
		layer++
		layer = layer % len(topology)
	}

	return topology
}

func (s *state) pruneDocuments() {
	// Looking a bit into the past is probably ok, if more past documents
	// need to be accessible, then methods that query the DB could always
	// be added.
	const preserveForPastEpochs = 3

	now, _, _ := epochtime.Now()
	cmpEpoch := now - preserveForPastEpochs

	for e := range s.documents {
		if e < cmpEpoch {
			delete(s.documents, e)
		}
	}
	for e := range s.descriptors {
		if e < cmpEpoch {
			delete(s.descriptors, e)
		}
	}
}

// Ensure that the descriptor is from the local registered node
func (s *state) isDescriptorAuthorized(desc *pki.MixDescriptor) bool {
	node := s.authorizedNode

	pk := hash.Sum256(desc.IdentityKey)
	if pk != hash.Sum256(node.IdentityKey) {
		s.log.Debugf("pki: ❌ isDescriptorAuthorized: IdentityKey mismatch for node %s", desc.Name)
		return false
	}

	if desc.IsGatewayNode != node.IsGatewayNode {
		return false
	}

	if desc.IsServiceNode != node.IsServiceNode {
		return false
	}

	return true
}

func (s *state) onDescriptorUpload(rawDesc []byte, desc *pki.MixDescriptor, epoch uint64) error {
	s.log.Noticef("pki: ⭐ onDescriptorUpload; Node name=%v, epoch=%v", desc.Name, epoch)
	s.Lock()
	defer s.Unlock()

	// Note: Caller ensures that the epoch is the current epoch +- 1.
	pk := hash.Sum256(desc.IdentityKey)

	// Get the public key -> descriptor map for the epoch.
	_, ok := s.descriptors[epoch]
	if !ok {
		s.descriptors[epoch] = make(map[[publicKeyHashSize]byte]*pki.MixDescriptor)
	}

	// Check for redundant uploads.
	d, ok := s.descriptors[epoch][pk]
	if ok {
		// If the descriptor changes, then it will be rejected to prevent
		// nodes from reneging on uploads.
		serialized, err := d.MarshalBinary()
		if err != nil {
			return err
		}
		if !hmac.Equal(serialized, rawDesc) {
			return fmt.Errorf("state: node %s (%x): Conflicting descriptor for epoch %v", desc.Name, hash.Sum256(desc.IdentityKey), epoch)
		}

		// Redundant uploads that don't change are harmless.
		return nil
	}

	// Ok, this is a new descriptor.
	if s.documents[epoch] != nil {
		// If there is a document already, the descriptor is late, and will
		// never appear in a document, so reject it.
		return fmt.Errorf("state: Node %v: Late descriptor upload for for epoch %v", desc.IdentityKey, epoch)
	}

	// Store the parsed descriptor
	s.descriptors[epoch][pk] = desc

	s.log.Noticef("Node %x: Successfully submitted descriptor for epoch %v.", pk, epoch)
	return nil
}

func (s *state) submitDescriptorToAppchain(desc *pki.MixDescriptor, epoch uint64) {
	// Register the mix descriptor with the appchain, which will:
	// - reject redundant descriptors (even those that didn't change)
	// - reject descriptors if document for the epoch exists
	if err := s.chPKISetMixDescriptor(desc, epoch); err != nil {
		s.log.Errorf("❌ submitDescriptorToAppchain: Failed to set mix descriptor for node %v, epoch=%v: %v", desc.Name, epoch, err)
	}
	epochCurrent, _, _ := epochtime.Now()
	s.log.Noticef("✅ submitDescriptorToAppchain: Submitted descriptor to appchain for node %v, epoch=%v (in epoch=%v)", desc.Name, epoch, epochCurrent)
}

func (s *state) documentForEpoch(epoch uint64) ([]byte, error) {
	s.log.Debugf("pki: documentForEpoch(%v)", epoch)

	s.RLock()
	defer s.RUnlock()

	// If we have a serialized document, return it.
	if d, ok := s.documents[epoch]; ok {
		// XXX We should cache this
		return d.MarshalCertificate()
	}

	// Otherwise, return an error based on the time.
	now, elapsed, _ := epochtime.Now()
	switch epoch {
	case now:
		// We missed the deadline to publish a descriptor for the current
		// epoch, so we will never be able to service this request.
		s.log.Errorf("No document for current epoch %v generated and never will be", now)
		return nil, errGone
	case now + 1:
		// If it's past the time by which we should have generated a document
		// then we will never be able to service this.
		if elapsed > DocGenerationDeadline {
			s.log.Errorf("No document for next epoch %v and it's already past DocGenerationDeadline of previous epoch", now+1)
			return nil, errGone
		}
		return nil, errNotYet
	default:
		if epoch < now {
			// Requested epoch is in the past, and it's not in the cache.
			// We will never be able to satisfy this request.
			s.log.Errorf("No document for epoch %v, because we are already in %v", epoch, now)
			return nil, errGone
		}
		return nil, fmt.Errorf("state: Request for invalid epoch: %v", epoch)
	}

	// NOTREACHED
}

func newState(s *Server) (*state, error) {
	st := new(state)
	st.s = s
	st.log = s.logBackend.GetLogger("state")

	// set voting schedule at runtime

	st.log.Debugf("State initialized with epoch Period: %s", epochtime.Period)
	st.log.Debugf("State initialized with RandomCourtessyDelay: %s", RandomCourtessyDelay)
	st.log.Debugf("State initialized with MixPublishDeadline: %s", MixPublishDeadline)
	st.log.Debugf("State initialized with DescriptorBlockDeadline: %s", DescriptorBlockDeadline)
	st.log.Debugf("State initialized with AuthorityVoteDeadline: %s", AuthorityVoteDeadline)
	st.log.Debugf("State initialized with PublishConsensusDeadline: %s", PublishConsensusDeadline)
	st.log.Debugf("State initialized with DocGenerationDeadline: %s", DocGenerationDeadline)

	ccbor, err := cbor.CanonicalEncOptions().EncMode()
	if err != nil {
		panic(err)
	}
	st.ccbor = ccbor

	// Init AppChain communications (chainbridge)
	chainBridgeLogger := s.logBackend.GetLogger("state:chain")
	st.chainBridge = chainbridge.NewChainBridge(filepath.Join(s.cfg.Server.DataDir, "appchain.sock"))
	st.chainBridge.SetErrorHandler(func(err error) {
		chainBridgeLogger.Errorf("Error: %v", err)
	})
	st.chainBridge.SetLogHandler(func(msg string) {
		chainBridgeLogger.Infof(msg)
	})
	if err := st.chainBridge.Start(); err != nil {
		chainBridgeLogger.Fatalf("Error: %v", err)
	}

	// Load the authorized local node from configuration

	// return a single node configuration and its node type
	extractNodeFromCfg := func() (*config.Node, bool, bool) {
		if len(st.s.cfg.GatewayNodes) == 1 {
			return st.s.cfg.GatewayNodes[0], true, false
		}
		if len(st.s.cfg.ServiceNodes) == 1 {
			return st.s.cfg.ServiceNodes[0], false, true
		}
		if len(st.s.cfg.Mixes) == 1 {
			return st.s.cfg.Mixes[0], false, false
		}
		return nil, false, false
	}
	v, isGatewayNode, isServiceNode := extractNodeFromCfg()
	if v == nil {
		st.log.Fatalf("❌ Error: Invalid configuration for a single local node")
	}

	pkiSignatureScheme := signSchemes.ByName(st.s.cfg.Server.PKISignatureScheme)
	var identityPublicKey sign.PublicKey
	if filepath.IsAbs(v.IdentityPublicKeyPem) {
		identityPublicKey, err = signpem.FromPublicPEMFile(v.IdentityPublicKeyPem, pkiSignatureScheme)
		if err != nil {
			panic(err)
		}
	} else {
		pemFilePath := filepath.Join(st.s.cfg.Server.DataDir, v.IdentityPublicKeyPem)
		identityPublicKey, err = signpem.FromPublicPEMFile(pemFilePath, pkiSignatureScheme)
		if err != nil {
			panic(err)
		}
	}

	// Node Registration: check if node is already registered before registering and rechecking
	st.authorizedNode, err = st.chNodesGet(v.Identifier)
	if err != nil {
		if err := st.chNodesRegister(v, identityPublicKey, isGatewayNode, isServiceNode); err != nil {
			st.log.Fatalf("❌ Error: node registration failed:", err)
		}
		time.Sleep(time.Duration(1) * time.Second)
		st.authorizedNode, err = st.chNodesGet(v.Identifier)
		if err != nil {
			s.log.Fatalf("❌ Error: Failed to get node=%s from appchain: %v", v.Identifier, err)
		}
	}

	// Ensure node appchain registration matches the local node configuration
	pk := hash.Sum256From(identityPublicKey)
	if pk != hash.Sum256(st.authorizedNode.IdentityKey) {
		s.log.Fatalf("❌ Error: IdentityKey mismatch between node registration and configuration")
	}

	st.log.Noticef("✅ Node registered with Identifier '%s', Identity key hash '%x'", v.Identifier, pk)

	st.log.Debugf("State initialized with epoch Period: %s", epochtime.Period)

	st.documents = make(map[uint64]*pki.Document)
	st.descriptors = make(map[uint64]map[[publicKeyHashSize]byte]*pki.MixDescriptor)

	epoch, elapsed, nextEpoch := epochtime.Now()
	st.log.Debugf("Epoch: %d, elapsed: %s, remaining time: %s", epoch, elapsed, nextEpoch)

	// Set the initial state to bootstrap
	st.state = stateBootstrap
	return st, nil
}

func (s *state) backgroundFetchConsensus(epoch uint64) {
	if s.TryLock() {
		panic("write lock not held in backgroundFetchConsensus(epoch)")
	}

	// If there isn't a consensus for the previous epoch, ask the appchain for a consensus.
	_, ok := s.documents[epoch]
	if !ok {
		s.Go(func() {
			doc, err := s.chPKIGetDocument(epoch)
			if err != nil {
				s.log.Debugf("pki: FetchConsensus: Failed to fetch document for epoch %v: %v", epoch, err)
				return
			}
			s.Lock()
			defer s.Unlock()

			// It's possible that the state has changed
			// if backgroundFetchConsensus was called
			// multiple times during bootstrapping
			if _, ok := s.documents[epoch]; !ok {
				// sign the locally-stored document
				_, err := s.doSignDocument(s.s.identityPrivateKey, s.s.identityPublicKey, doc)
				if err != nil {
					s.log.Errorf("pki: FetchConsensus: Error signing document for epoch %v: %v", epoch, err)
					return
				}
				s.documents[epoch] = doc
				s.log.Debugf("pki: FetchConsensus: ✅ Set doc for epoch %v: %s", epoch, doc.String())
			}
		})
	}
}

func sortNodesByPublicKey(nodes []*pki.MixDescriptor) {
	dTos := func(d *pki.MixDescriptor) string {
		pk := hash.Sum256(d.IdentityKey)
		return string(pk[:])
	}
	sort.Slice(nodes, func(i, j int) bool { return dTos(nodes[i]) < dTos(nodes[j]) })
}
