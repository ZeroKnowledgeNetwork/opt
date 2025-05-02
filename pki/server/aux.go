/****** ZK-PKI ******/
// ZK-PKI specifics for minimal changes to the upstream code

package server

import (
	"math"
	"path/filepath"
	"time"

	"github.com/ZeroKnowledgeNetwork/appchain-agent/clients/go/chainbridge"
	"github.com/katzenpost/hpqc/hash"
	"github.com/katzenpost/hpqc/rand"
	"github.com/katzenpost/hpqc/sign"
	signpem "github.com/katzenpost/hpqc/sign/pem"
	signSchemes "github.com/katzenpost/hpqc/sign/schemes"
	"github.com/katzenpost/katzenpost/authority/voting/server/config"
	"github.com/katzenpost/katzenpost/core/epochtime"
	"github.com/katzenpost/katzenpost/core/pki"
)

const (
	stateBootstrap        = "bootstrap"         // register node, fetch consensus for previous epoch
	stateSubmitDescriptor = "submit_descriptor" // submit mix descriptor to appchain (wait for block inclusion)
	stateCommitVote       = "commit_vote"       // retrieve descriptors from appchain, commit pki doc as hidden "vote" to appchain
	stateRevealVote       = "reveal_vote"       // reveal vote to appchain, establish threshold consensus
	stateAcceptVote       = "accept_vote"       // retrieve pki doc from appchain, with highest vote count
	stateConfirmConsensus = "confirm_consensus" // confirm doc reception
)

// NOTE: 2024-11-01:
// Parts of katzenpost use MixPublishDeadline and PublishConsensusDeadline defined in
// katzenpost:authority/voting/server/state.go
// So, we preserve that aspect of the epoch schedule.
var (
	DeadlineMixPublish          = epochtime.Period * 1 / 8 // Do NOT change this
	DeadlineDescriptorProcessed = epochtime.Period * 2 / 8
	DeadlineAuthorityVote       = epochtime.Period * 3 / 8
	DeadlineAuthorityReveal     = epochtime.Period * 4 / 8
	DeadlinePublishConsensus    = epochtime.Period * 5 / 8 // Do NOT change this
	DeadlineDocGeneration       = epochtime.Period * 7 / 8 // Do NOT change this (see katzenpost:authority/voting/server/state.go:documentForEpoch)
)

var (
	JitterMax = epochtime.Period * 1 / 16 // max duration to distribute network load across synchronized nodes
)

func (s *state) fsm() <-chan time.Time {
	s.Lock()
	var sleep time.Duration
	epoch, elapsed, nextEpoch := epochtime.Now()
	s.zlog.Debugf("Current epoch %d, remaining time: %s, state: %s", epoch, nextEpoch, s.state)

	nextDeadline := func(window time.Duration) time.Duration {
		_, nowelapsed, _ := epochtime.Now()
		d := window - nowelapsed + s.zkpki_jitter()
		if d < 0 {
			return 0
		}
		return d
	}

	switch s.state {
	case stateBootstrap:
		// TODO: ensure network is ready and locally registered node is eligible for participation
		s.genesisEpoch = 0
		s.backgroundFetchConsensus(epoch - 1)
		s.backgroundFetchConsensus(epoch)
		if elapsed > DeadlineMixPublish {
			s.zlog.Errorf("Too late to vote this round, sleeping until %s", nextEpoch)
			sleep = nextEpoch
			s.votingEpoch = epoch + 2
			s.state = stateBootstrap
		} else {
			s.votingEpoch = epoch + 1
			s.state = stateSubmitDescriptor
			sleep = nextDeadline(DeadlineMixPublish)
			s.zlog.Noticef("Bootstrapping for %d", s.votingEpoch)
		}

	case stateSubmitDescriptor:
		pk := hash.Sum256(s.authorizedNode.IdentityKey)
		desc, ok := s.descriptors[s.votingEpoch][pk]
		if ok {
			s.zkpki_submitDescriptor(desc, s.votingEpoch)
		} else {
			s.zlog.Errorf("❌ No descriptor for epoch %d", s.votingEpoch)
		}
		s.state = stateCommitVote
		sleep = nextDeadline(DeadlineDescriptorProcessed)

	case stateCommitVote:
		doc, err := s.getVote(s.votingEpoch)
		if err == nil {
			s.zkpki_commitVote(doc, s.votingEpoch)
		} else {
			s.zlog.Errorf("❌ Failed to compute vote for epoch %v: %s", s.votingEpoch, err)
		}
		s.state = stateRevealVote
		sleep = nextDeadline(DeadlineAuthorityVote)

	case stateRevealVote:
		doc, err := s.getVote(s.votingEpoch)
		if err == nil {
			s.zkpki_revealVote(doc, s.votingEpoch)
		} else {
			s.zlog.Errorf("❌ Failed to compute vote for epoch %v: %s", s.votingEpoch, err)
		}
		s.state = stateAcceptVote
		sleep = nextDeadline(DeadlineAuthorityReveal)

	case stateAcceptVote:
		s.backgroundFetchConsensus(s.votingEpoch)
		s.state = stateConfirmConsensus
		sleep = nextDeadline(DeadlinePublishConsensus)

	case stateConfirmConsensus:
		// See if consensus doc was retrieved from the appchain
		_, ok := s.documents[epoch+1]
		if ok {
			s.state = stateSubmitDescriptor
			sleep = DeadlineMixPublish + nextEpoch + s.zkpki_jitter()
			s.votingEpoch++
		} else {
			s.zlog.Error("No document for epoch %v", epoch+1)
			s.state = stateBootstrap
			s.votingEpoch = epoch + 2 // vote on epoch+2 in epoch+1
			sleep = nextEpoch
		}

	default:
	}

	s.pruneDocuments()
	s.zlog.Debugf("authority: FSM in state %v until %s", s.state, sleep)
	s.Unlock()
	return time.After(sleep)
}

// related: pki/server/server.go:computeLambdaG
// compute lambdaG value for dynamic topology from number of nodes per layer, and not from config file
func zkpki_computeLambdaGFromNodesPerLayer(cfg *config.Config, npl int) float64 {
	n := float64(npl)
	if n == 1 {
		return cfg.Parameters.LambdaP + cfg.Parameters.LambdaL + cfg.Parameters.LambdaD
	}
	return n * math.Log(n)
}

// Returns a random delay to distribute load across synchronized nodes
func (s *state) zkpki_jitter() time.Duration {
	return time.Duration(rand.NewMath().Float64() * float64(JitterMax))
}

func (s *state) zkpki_submitDescriptor(desc *pki.MixDescriptor, epoch uint64) {
	// Register the mix descriptor with the appchain, which will:
	// - reject redundant descriptors (even those that didn't change)
	// - reject descriptors if document for the epoch exists
	if err := s.chPKISetMixDescriptor(desc, epoch); err != nil {
		s.zlog.Errorf("❌ submitDescriptor: Failed to set mix descriptor for node %v, epoch=%v: %v", desc.Name, epoch, err)
	}
	epochCurrent, _, _ := epochtime.Now()
	s.zlog.Noticef("✅ submitDescriptor: Set mix descriptor for node %v, epoch=%v (in epoch=%v)", desc.Name, epoch, epochCurrent)
}

func (s *state) zkpki_sendVote(doc *pki.Document, epoch uint64) {
	if err := s.chPKISetDocument(doc); err != nil {
		s.zlog.Errorf("❌ sendVote: Error setting document for epoch %d: %v", epoch, err)
	} else {
		s.zlog.Noticef("✅ sendVote: Set document for epoch %d", epoch)
	}
}

func (s *state) zkpki_commitVote(doc *pki.Document, epoch uint64) {
	if err := s.chConsensusCommitVote(doc); err != nil {
		s.zlog.Errorf("❌ commitVote: epoch %d: %v", epoch, err)
	} else {
		s.zlog.Noticef("✅ commitVote: epoch %d", epoch)
	}
}

func (s *state) zkpki_revealVote(doc *pki.Document, epoch uint64) {
	if err := s.chConsensusRevealVote(doc); err != nil {
		s.zlog.Errorf("❌ revealVote: epoch %d: %v", epoch, err)
	} else {
		s.zlog.Noticef("✅ revealVote: epoch %d", epoch)
	}
}

func (s *state) zkpki_backgroundFetchConsensus(epoch uint64) {
	// If there isn't a consensus for the previous epoch, ask the appchain for a consensus.
	_, ok := s.documents[epoch]
	if !ok {
		s.Go(func() {
			doc, err := s.chConsensusGetConsensus(epoch)
			if err != nil {
				s.zlog.Debugf("FetchConsensus: Failed to fetch document for epoch %v: %v", epoch, err)
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
					s.zlog.Errorf("FetchConsensus: Error signing document for epoch %v: %v", epoch, err)
					return
				}
				s.documents[epoch] = doc
				s.zlog.Debugf("FetchConsensus: ✅ Set local doc for epoch %v: %s", epoch, doc.String())
			}
		})
	}
}

// produces a pki.Document using all MixDescriptors from the appchain
func (s *state) zkpki_getVote(epoch uint64) (*pki.Document, error) {
	descriptors, err := s.chPKIGetMixDescriptors(epoch)
	if err != nil {
		return nil, err
	}

	// TODO replicaDescriptors not yet recorded to appchain
	replicaDescriptors := []*pki.ReplicaDescriptor{}

	// vote topology is irrelevent.
	// TODO: idea: use an appchain block hash as srv
	var zeros [32]byte
	doc := s.getDocument(descriptors, replicaDescriptors, s.s.cfg.Parameters, zeros[:])

	// Note: upload unsigned document and sign it upon local save to use the doc CID as the vote.

	// simulate SignDocument's setting of doc version, required by IsDocumentWellFormed
	doc.Version = pki.DocumentVersion

	if err := pki.IsDocumentWellFormed(doc, nil); err != nil {
		s.zlog.Errorf("❌ getVote: IsDocumentWellFormed: %s", err)
		return nil, err
	}

	return doc, nil
}

func zkpki_newState(st *state) error {
	st.zlog = st.s.logBackend.GetLogger("state/zkpki")

	st.zlog.Debugf("State initialized with epoch Period: %s", epochtime.Period)
	st.zlog.Debugf("State initialized with JitterMax: %s", JitterMax)
	st.zlog.Debugf("State initialized with DeadlineMixPublish: %s", DeadlineMixPublish)
	st.zlog.Debugf("State initialized with DeadlineDescriptorProcessed: %s", DeadlineDescriptorProcessed)
	st.zlog.Debugf("State initialized with DeadlineAuthorityVote: %s", DeadlineAuthorityVote)
	st.zlog.Debugf("State initialized with DeadlinePublishConsensus: %s", DeadlinePublishConsensus)
	st.zlog.Debugf("State initialized with DeadlineDocGeneration: %s", DeadlineDocGeneration)

	// Init AppChain communications (chainbridge)
	chlog := st.s.logBackend.GetLogger("state/zkpki/chain")
	st.chainBridge = chainbridge.NewChainBridge(filepath.Join(st.s.cfg.Server.DataDir, "appchain.sock"))
	st.chainBridge.SetErrorHandler(func(err error) {
		chlog.Errorf("Error: %v", err)
	})
	st.chainBridge.SetLogHandler(func(msg string) {
		chlog.Infof(msg)
	})
	if err := st.chainBridge.Start(); err != nil {
		chlog.Fatalf("Error: %v", err)
	}

	// Load the authorized local node from configuration

	// return a single node configuration and its node type
	extractNodeFromCfg := func() (*config.Node, bool, bool, bool) {
		if len(st.s.cfg.GatewayNodes) == 1 {
			return st.s.cfg.GatewayNodes[0], true, false, false
		}
		if len(st.s.cfg.ServiceNodes) == 1 {
			return st.s.cfg.ServiceNodes[0], false, true, false
		}
		if len(st.s.cfg.Mixes) == 1 {
			return st.s.cfg.Mixes[0], false, false, false
		}
		if len(st.s.cfg.StorageReplicas) == 1 {
			return st.s.cfg.StorageReplicas[0], false, false, true
		}
		return nil, false, false, false
	}
	v, isGatewayNode, isServiceNode, _ /*isStorageReplica*/ := extractNodeFromCfg()
	if v == nil {
		st.zlog.Fatalf("❌ Error: Invalid configuration for a single local node")
	}

	// load the authorized node's identity public key
	pkiSignatureScheme := signSchemes.ByName(st.s.cfg.Server.PKISignatureScheme)
	var err error
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
			st.zlog.Fatalf("❌ Error: node registration failed:", err)
		}
		time.Sleep(time.Duration(1) * time.Second)
		st.authorizedNode, err = st.chNodesGet(v.Identifier)
		if err != nil {
			st.zlog.Fatalf("❌ Error: Failed to get node=%s from appchain: %v", v.Identifier, err)
		}
	}

	// Ensure node appchain registration matches the local node configuration
	pk := hash.Sum256From(identityPublicKey)
	if pk != hash.Sum256(st.authorizedNode.IdentityKey) {
		st.zlog.Fatalf("❌ Error: IdentityKey mismatch between node registration and configuration")
	}

	st.zlog.Noticef("✅ Node registered with Identifier '%s', Identity key hash '%x'", v.Identifier, pk)

	// Register node as voter of pki consensus
	if err = st.chConsensusRegister(); err != nil {
		st.zlog.Fatalf("❌ Error: consensus registration failed:", err)
	}

	return nil
}
