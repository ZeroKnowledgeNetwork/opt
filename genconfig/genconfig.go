// related: katzenpost:genconfig/main.go

// SPDX-FileCopyrightText: Copyright (C) 2022  Yawning Angel, David Stainton, Masala
// SPDX-License-Identifier: AGPL-3.0-only

package genconfig

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"github.com/katzenpost/hpqc/hash"
	"github.com/katzenpost/hpqc/kem"
	kempem "github.com/katzenpost/hpqc/kem/pem"
	kemschemes "github.com/katzenpost/hpqc/kem/schemes"
	"github.com/katzenpost/hpqc/nike/schemes"
	"github.com/katzenpost/hpqc/sign"
	signpem "github.com/katzenpost/hpqc/sign/pem"
	signSchemes "github.com/katzenpost/hpqc/sign/schemes"

	vConfig "github.com/katzenpost/katzenpost/authority/voting/server/config"
	cConfig "github.com/katzenpost/katzenpost/client/config"
	cConfig2 "github.com/katzenpost/katzenpost/client2/config"
	cpki "github.com/katzenpost/katzenpost/core/pki"
	"github.com/katzenpost/katzenpost/core/sphinx/geo"
	sConfig "github.com/katzenpost/katzenpost/server/config"

	zknConfig "github.com/ZeroKnowledgeNetwork/opt/genconfig/config"
)

const (
	addr      = "127.0.0.1"
	basePort  = 8080
	transport = "tcp"
	metrics   = "0.0.0.0:9100"
)

type GenconfigInput struct {
	addr             string
	addrBind         string
	baseDir          string
	basePort         int
	binPrefix        string
	binSuffix        string
	cfgType          string
	identifier       string
	inputNetworkInfo string
	logLevel         string
	metrics          string
	outDir           string
	transport        string
}

type katzenpost struct {
	baseDir   string
	outDir    string
	binPrefix string
	binSuffix string
	logLevel  string
	logWriter io.Writer

	ratchetNIKEScheme  string
	wireKEMScheme      string
	pkiSignatureScheme sign.Scheme
	sphinxGeometry     *geo.Geometry
	votingAuthConfigs  []*vConfig.Config
	authorities        map[[32]byte]*vConfig.Authority
	authIdentity       sign.PublicKey

	nodeConfigs    []*sConfig.Config
	basePort       uint16
	lastPort       uint16
	addr           string
	addrBind       string
	nodeIdx        int
	clientIdx      int
	gatewayIdx     int
	serviceNodeIdx int
	hasPanda       bool
	hasProxy       bool
	transport      string
	metrics        string

	debugConfigClient1 *cConfig.Debug
	debugConfigClient2 *cConfig2.Debug
	debugConfigServer  *sConfig.Debug
}

type AuthById []*vConfig.Authority

func (a AuthById) Len() int           { return len(a) }
func (a AuthById) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a AuthById) Less(i, j int) bool { return a[i].Identifier < a[j].Identifier }

type NodeById []*vConfig.Node

func (a NodeById) Len() int           { return len(a) }
func (a NodeById) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a NodeById) Less(i, j int) bool { return a[i].Identifier < a[j].Identifier }

func addressesFromURLs(addrs []string) map[string][]string {
	addresses := make(map[string][]string)
	for _, addr := range addrs {
		u, err := url.Parse(addr)
		if err != nil {
			continue
		}
		switch u.Scheme {
		case cpki.TransportTCP, cpki.TransportTCPv4, cpki.TransportTCPv6, cpki.TransportQUIC:
			if _, ok := addresses[u.Scheme]; !ok {
				addresses[u.Scheme] = make([]string, 0)
			}
			addresses[u.Scheme] = append(addresses[u.Scheme], u.String())
		default:
			continue
		}
	}
	return addresses
}

func (s *katzenpost) genClient2Cfg() error {
	os.Mkdir(filepath.Join(s.outDir, "client2"), 0700)
	cfg := new(cConfig2.Config)

	// abstract unix domain sockets only work on linux,
	//cfg.ListenNetwork = "unix"
	//cfg.ListenAddress = "@katzenpost"
	// therefore if unix sockets are requires on non-linux platforms
	// the solution is to specify a unix socket file path instead of
	// and abstract unix socket name:
	//cfg.ListenNetwork = "unix"
	//cfg.ListenAddress = "/tmp/katzenzpost.socket"

	// Use TCP by default so that the CI tests pass on all platforms
	cfg.ListenNetwork = "tcp"
	cfg.ListenAddress = "localhost:64331"

	cfg.PKISignatureScheme = s.pkiSignatureScheme.Name()
	cfg.WireKEMScheme = s.wireKEMScheme
	cfg.SphinxGeometry = s.sphinxGeometry

	// Logging section.
	cfg.Logging = &cConfig2.Logging{File: "", Level: "DEBUG"}

	// UpstreamProxy section
	cfg.UpstreamProxy = &cConfig2.UpstreamProxy{Type: "none"}

	// VotingAuthority section

	peers := make([]*vConfig.Authority, 0)
	for _, peer := range s.authorities {
		peers = append(peers, peer)
	}

	sort.Sort(AuthById(peers))

	cfg.VotingAuthority = &cConfig2.VotingAuthority{Peers: peers}

	// Debug section
	cfg.Debug = s.debugConfigClient2

	gateways := make([]*cConfig2.Gateway, 0)
	for i := 0; i < len(s.nodeConfigs); i++ {
		if s.nodeConfigs[i].Gateway == nil {
			continue
		}

		idPubKey := cfgIdKey(s.nodeConfigs[i], s.outDir)
		linkPubKey := cfgLinkKey(s.nodeConfigs[i], s.outDir, cfg.WireKEMScheme)

		gateway := &cConfig2.Gateway{
			PKISignatureScheme: s.pkiSignatureScheme.Name(),
			WireKEMScheme:      s.wireKEMScheme,
			Name:               s.nodeConfigs[i].Server.Identifier,
			IdentityKey:        idPubKey,
			LinkKey:            linkPubKey,
			Addresses:          s.nodeConfigs[i].Server.Addresses,
		}
		gateways = append(gateways, gateway)
	}
	cfg.PinnedGateways = &cConfig2.Gateways{
		Gateways: gateways,
	}

	err := saveCfg(cfg, s.outDir)
	if err != nil {
		log.Printf("save config failure %s", err.Error())
		return err
	}
	return nil
}

func (s *katzenpost) genClientCfg() error {
	os.Mkdir(filepath.Join(s.outDir, "client"), 0700)
	cfg := new(cConfig.Config)

	cfg.RatchetNIKEScheme = s.ratchetNIKEScheme
	cfg.WireKEMScheme = s.wireKEMScheme
	cfg.PKISignatureScheme = s.pkiSignatureScheme.Name()
	cfg.SphinxGeometry = s.sphinxGeometry

	s.clientIdx++

	// Logging section.
	cfg.Logging = &cConfig.Logging{File: "", Level: s.logLevel}

	// UpstreamProxy section
	cfg.UpstreamProxy = &cConfig.UpstreamProxy{Type: "none"}

	// VotingAuthority section

	peers := make([]*vConfig.Authority, 0)
	for _, peer := range s.authorities {
		peers = append(peers, peer)
	}

	sort.Sort(AuthById(peers))

	cfg.VotingAuthority = &cConfig.VotingAuthority{Peers: peers}

	// Debug section
	cfg.Debug = s.debugConfigClient1
	err := saveCfg(cfg, s.outDir)
	if err != nil {
		return err
	}
	return nil
}

func write(f *os.File, str string, args ...interface{}) {
	str = fmt.Sprintf(str, args...)
	_, err := f.WriteString(str)

	if err != nil {
		log.Fatal(err)
	}
}

func (s *katzenpost) genNodeConfig(identifier string, isGateway bool, isServiceNode bool, isVoting bool) error {
	const serverLogFile = "katzenpost.log"

	n := identifier

	cfg := new(sConfig.Config)
	cfg.SphinxGeometry = s.sphinxGeometry

	// Server section.
	cfg.Server = new(sConfig.Server)
	cfg.Server.WireKEM = s.wireKEMScheme
	cfg.Server.PKISignatureScheme = s.pkiSignatureScheme.Name()
	cfg.Server.Identifier = n
	cfg.Server.Addresses = []string{fmt.Sprintf("%s://%s:%d", s.transport, s.addr, s.basePort)}
	if s.addrBind != "" {
		cfg.Server.BindAddresses = []string{fmt.Sprintf("%s://%s:%d", s.transport, s.addrBind, s.basePort)}
	}
	cfg.Server.DataDir = filepath.Join(s.baseDir, n)

	os.Mkdir(filepath.Join(s.outDir, cfg.Server.Identifier), 0700)

	cfg.Server.IsGatewayNode = isGateway
	cfg.Server.IsServiceNode = isServiceNode
	if isGateway {
		cfg.Management = new(sConfig.Management)
		cfg.Management.Enable = true
	}
	if isServiceNode {
		cfg.Management = new(sConfig.Management)
		cfg.Management.Enable = true
	}
	// Enable Metrics endpoint
	cfg.Server.MetricsAddress = s.metrics

	// Debug section.
	cfg.Debug = s.debugConfigServer

	// PKI section.
	if isVoting {
		authorities := make([]*vConfig.Authority, 0, len(s.authorities))
		i := 0
		for _, auth := range s.authorities {
			authorities = append(authorities, auth)
			i += 1
		}

		sort.Sort(AuthById(authorities))
		cfg.PKI = &sConfig.PKI{
			Voting: &sConfig.Voting{
				Authorities: authorities,
			},
		}
	}

	// Logging section.
	cfg.Logging = new(sConfig.Logging)
	cfg.Logging.File = serverLogFile
	cfg.Logging.Level = s.logLevel

	if isServiceNode {
		// Enable the thwack interface.
		s.serviceNodeIdx++

		// configure an entry provider or a spool storage provider
		cfg.ServiceNode = &sConfig.ServiceNode{}
		spoolCfg := &sConfig.CBORPluginKaetzchen{
			Capability:     "spool",
			Endpoint:       "+spool",
			Command:        s.binPrefix + "memspool" + s.binSuffix,
			MaxConcurrency: 1,
			Config: map[string]interface{}{
				"data_store": s.baseDir + "/" + cfg.Server.Identifier + "/memspool.storage",
				"log_dir":    s.baseDir + "/" + cfg.Server.Identifier,
			},
		}
		cfg.ServiceNode.CBORPluginKaetzchen = []*sConfig.CBORPluginKaetzchen{spoolCfg}
		if !s.hasPanda {
			mapCfg := &sConfig.CBORPluginKaetzchen{
				Capability:     "pigeonhole",
				Endpoint:       "+pigeonhole",
				Command:        s.binPrefix + "pigeonhole" + s.binSuffix,
				MaxConcurrency: 1,
				Config: map[string]interface{}{
					"db":      s.baseDir + "/" + cfg.Server.Identifier + "/map.storage",
					"log_dir": s.baseDir + "/" + cfg.Server.Identifier,
				},
			}

			cfg.ServiceNode.CBORPluginKaetzchen = []*sConfig.CBORPluginKaetzchen{spoolCfg, mapCfg}
			if !s.hasPanda {
				pandaCfg := &sConfig.CBORPluginKaetzchen{
					Capability:     "panda",
					Endpoint:       "+panda",
					Command:        s.binPrefix + "panda_server" + s.binSuffix,
					MaxConcurrency: 1,
					Config: map[string]interface{}{
						"fileStore": s.baseDir + "/" + cfg.Server.Identifier + "/panda.storage",
						"log_dir":   s.baseDir + "/" + cfg.Server.Identifier,
						"log_level": s.logLevel,
					},
				}
				cfg.ServiceNode.CBORPluginKaetzchen = append(cfg.ServiceNode.CBORPluginKaetzchen, pandaCfg)
				s.hasPanda = true
			}

			// Add a single instance of a http proxy for a service listening on port 4242
			if !s.hasProxy {
				proxyCfg := &sConfig.CBORPluginKaetzchen{
					Capability:     "http",
					Endpoint:       "+http",
					Command:        s.binPrefix + "proxy_server" + s.binSuffix,
					MaxConcurrency: 1,
					Config: map[string]interface{}{
						// allow connections to localhost:4242
						"host":      "localhost:4242",
						"log_dir":   s.baseDir + "/" + cfg.Server.Identifier,
						"log_level": "DEBUG",
					},
				}
				cfg.ServiceNode.CBORPluginKaetzchen = append(cfg.ServiceNode.CBORPluginKaetzchen, proxyCfg)
				s.hasProxy = true
			}

			// ZKN JSON RPC - HTTP Proxy
			httpProxyCfg := &sConfig.CBORPluginKaetzchen{
				Capability:     "http_proxy",
				Endpoint:       "http_proxy",
				Command:        s.binPrefix + "http_proxy" + s.binSuffix,
				MaxConcurrency: 1,
				Disable:        false,
				Config: map[string]interface{}{
					"config":  s.baseDir + "/" + cfg.Server.Identifier + "/http_proxy_config.toml",
					"log_dir": s.baseDir + "/" + cfg.Server.Identifier,
				},
			}
			cfg.ServiceNode.CBORPluginKaetzchen = append(cfg.ServiceNode.CBORPluginKaetzchen, httpProxyCfg)
			// create empty default http_proxy_config.toml file
			httpProxyConfigFile := filepath.Join(s.outDir, cfg.Server.Identifier, "http_proxy_config.toml")
			saveFileContents(httpProxyConfigFile, "[Networks]\n")

			cfg.Debug.NumKaetzchenWorkers = 4
		}

		echoCfg := new(sConfig.Kaetzchen)
		echoCfg.Capability = "echo"
		echoCfg.Endpoint = "+echo"
		cfg.ServiceNode.Kaetzchen = append(cfg.ServiceNode.Kaetzchen, echoCfg)
		testdestCfg := new(sConfig.Kaetzchen)
		testdestCfg.Capability = "testdest"
		testdestCfg.Endpoint = "+testdest"
		cfg.ServiceNode.Kaetzchen = append(cfg.ServiceNode.Kaetzchen, testdestCfg)

	} else if isGateway {
		s.gatewayIdx++
		cfg.Gateway = &sConfig.Gateway{}
	} else {
		s.nodeIdx++
	}
	s.nodeConfigs = append(s.nodeConfigs, cfg)
	_ = cfgIdKey(cfg, s.outDir)
	_ = cfgLinkKey(cfg, s.outDir, s.wireKEMScheme)
	return cfg.FixupAndValidate()
}

func (s *katzenpost) genVotingAuthoritiesCfg(identifier string, numAuthorities int, parameters *vConfig.Parameters, nrLayers int, wirekem string) error {

	configs := []*vConfig.Config{}

	// initial generation of key material for each authority
	s.authorities = make(map[[32]byte]*vConfig.Authority)
	for i := 1; i <= numAuthorities; i++ {
		cfg := new(vConfig.Config)
		cfg.SphinxGeometry = s.sphinxGeometry
		cfg.Server = &vConfig.Server{
			WireKEMScheme:      s.wireKEMScheme,
			PKISignatureScheme: s.pkiSignatureScheme.Name(),
			Identifier:         identifier,
			Addresses:          []string{fmt.Sprintf("%s://127.0.0.1:%d", s.transport, s.lastPort)},
			DataDir:            filepath.Join(s.baseDir, identifier),
		}
		os.Mkdir(filepath.Join(s.outDir, cfg.Server.Identifier), 0700)
		s.lastPort += 1
		cfg.Logging = &vConfig.Logging{
			Disable: false,
			File:    "katzenpost.log",
			Level:   s.logLevel,
		}
		cfg.Parameters = parameters
		cfg.Debug = &vConfig.Debug{
			Layers:           nrLayers,
			MinNodesPerLayer: 1,
			GenerateOnly:     false,
		}
		configs = append(configs, cfg)
		idKey := cfgIdKey(cfg, s.outDir)
		linkKey := cfgLinkKey(cfg, s.outDir, wirekem)
		authority := &vConfig.Authority{
			Identifier:         identifier,
			IdentityPublicKey:  idKey,
			LinkPublicKey:      linkKey,
			WireKEMScheme:      wirekem,
			PKISignatureScheme: s.pkiSignatureScheme.Name(),
			Addresses:          cfg.Server.Addresses,
		}
		s.authorities[hash.Sum256From(idKey)] = authority
	}

	// tell each authority about it's peers
	for i := 0; i < numAuthorities; i++ {
		peers := []*vConfig.Authority{}
		for _, peer := range s.authorities {
			peers = append(peers, peer)
		}
		sort.Sort(AuthById(peers))
		configs[i].Authorities = peers
	}
	s.votingAuthConfigs = configs
	return nil
}

func (s *katzenpost) genAuthorizedNodes() ([]*vConfig.Node, []*vConfig.Node, []*vConfig.Node, error) {
	mixes := []*vConfig.Node{}
	gateways := []*vConfig.Node{}
	serviceNodes := []*vConfig.Node{}
	for _, nodeCfg := range s.nodeConfigs {
		node := &vConfig.Node{
			Identifier:           nodeCfg.Server.Identifier,
			IdentityPublicKeyPem: filepath.Join("..", nodeCfg.Server.Identifier, "identity.public.pem"),
			// Note: 20250421 KP config.Load does not support Abs path for this key, so use relative
			// IdentityPublicKeyPem: filepath.Join(s.outDir, nodeCfg.Server.Identifier, "identity.public.pem"),
		}
		if nodeCfg.Server.IsGatewayNode {
			gateways = append(gateways, node)
		} else if nodeCfg.Server.IsServiceNode {
			serviceNodes = append(serviceNodes, node)
		} else {
			mixes = append(mixes, node)
		}
	}
	sort.Sort(NodeById(mixes))
	sort.Sort(NodeById(gateways))
	sort.Sort(NodeById(serviceNodes))

	return gateways, serviceNodes, mixes, nil
}

func identifierIsValid(s string) bool {
	pattern := `^[a-z0-9](?:[a-z0-9\-]{2,18}[a-z0-9])$`
	re := regexp.MustCompile(pattern)
	return re.MatchString(s)
}

func ParseFlags() GenconfigInput {
	var gi GenconfigInput
	flag.IntVar(&gi.basePort, "port", basePort, "First port number to use")
	flag.StringVar(&gi.addr, "address", addr, "Address to publish (and bind to if -address-bind not set)")
	flag.StringVar(&gi.addrBind, "address-bind", "", "Address to bind to")
	flag.StringVar(&gi.baseDir, "dir-base", "", "Absolute path as installation directory in config files (default -dir-out)")
	flag.StringVar(&gi.binPrefix, "binary-prefix", "", "Prefix for binaries")
	flag.StringVar(&gi.binSuffix, "binary-suffix", "", "Suffix for binaries")
	flag.StringVar(&gi.cfgType, "type", "", "Type of config to generate: mix, gateway, servicenode, client1, client2")
	flag.StringVar(&gi.identifier, "identifier", "", "Node identifier; lowercase alphanumeric with 4 to 20 characters (default -type)")
	flag.StringVar(&gi.inputNetworkInfo, "input", "network.yml", "Path to network info file")
	flag.StringVar(&gi.logLevel, "log-level", "INFO", "logging level could be set to: DEBUG, INFO, NOTICE, WARNING, ERROR, CRITICAL")
	flag.StringVar(&gi.metrics, "metrics", metrics, "Metrics endpoint")
	flag.StringVar(&gi.outDir, "dir-out", "", "Path to write files to")
	flag.StringVar(&gi.transport, "transport", transport, "Transport protocol: tcp, quic")

	flag.Parse()

	if gi.baseDir == "" {
		gi.baseDir = gi.outDir
	}

	return gi
}

func Genconfig(gi GenconfigInput) error {
	// tmp hack: these are here to minimize other changes to the original...
	nrLayers := new(int)
	nrNodes := new(int)
	nrGateways := new(int)
	nrServiceNodes := new(int)
	voting := new(bool)
	nrVoting := new(int)
	omitTopology := new(bool)

	addr := &gi.addr
	addrBind := &gi.addrBind
	baseDir := &gi.baseDir
	basePort := &gi.basePort
	binPrefix := &gi.binPrefix
	binSuffix := &gi.binSuffix
	cfgType := &gi.cfgType
	identifier := &gi.identifier
	logLevel := &gi.logLevel
	metrics := &gi.metrics
	outDir := &gi.outDir
	transport := &gi.transport

	// Read the network info file
	data, err := os.ReadFile(gi.inputNetworkInfo)
	if err != nil {
		return errors.New(fmt.Sprintf("Error reading file: %v\n", err))
	}
	var networkInfo zknConfig.NetworkInfo
	err = yaml.Unmarshal(data, &networkInfo)
	if err != nil {
		return errors.New(fmt.Sprintf("Error decoding YAML: %v\n", err))
	}

	*nrLayers = networkInfo.KpMiscTopologyLayers
	*omitTopology = true                                          // "Dynamic topology (omit fixed topology definition)"
	wirekem := &networkInfo.KpConfigWirekem                       // "Name of the KEM Scheme to be used with wire protocol"
	kem := &networkInfo.KpConfigKem                               // "Name of the KEM Scheme to be used with Sphinx"
	nike := &networkInfo.KpConfigNike                             // "Name of the NIKE Scheme to be used with Sphinx"
	ratchetNike := &networkInfo.KpConfigRatchetNike               // "Name of the NIKE Scheme to be used with the doubleratchet"
	pkiSignatureScheme := &networkInfo.KpConfigPkiSignatureScheme // "PKI Signature Scheme to be used"
	UserForwardPayloadLength := &networkInfo.KpConfigUserForwardPayloadLength

	if *wirekem == "" {
		return errors.New("wire KEM must be set")
	}

	if *kem == "" && *nike == "" {
		return errors.New("either nike or kem must be set")
	}
	if *kem != "" && *nike != "" {
		return errors.New("nike and kem flags cannot both be set")
	}

	if *ratchetNike == "" {
		return errors.New("ratchetNike must be set")
	}

	if *pkiSignatureScheme == "" {
		return errors.New("pkiSignatureScheme must be set")
	}

	if *identifier != "" && !identifierIsValid(*identifier) {
		return errors.New(fmt.Sprintf("Invalid identifier: %s", *identifier))
	}

	// generate config for a single node of the given type, each with its own authority
	*nrNodes = 0
	*nrGateways = 0
	*nrServiceNodes = 0
	*nrVoting = 1
	*voting = true
	switch *cfgType {
	case "":
		return errors.New("type must be set")
	case "mix":
		*nrNodes = 1
	case "gateway":
		*nrGateways = 1
	case "servicenode":
		*nrServiceNodes = 1
	case "client1", "client2":
		*voting = false
	default:
		return errors.New("invalid type")
	}

	if *identifier == "" {
		identifier = cfgType
	}

	parameters := &vConfig.Parameters{
		SendRatePerMinute: networkInfo.KpConfigSendRatePerMinute,
		Mu:                networkInfo.KpConfigMu,
		MuMaxDelay:        networkInfo.KpConfigMuMaxDelay,
		LambdaP:           networkInfo.KpConfigLambdaP,
		LambdaPMaxDelay:   networkInfo.KpConfigLambdaPMaxDelay,
		LambdaL:           networkInfo.KpConfigLambdaL,
		LambdaLMaxDelay:   networkInfo.KpConfigLambdaLMaxDelay,
		LambdaD:           networkInfo.KpConfigLambdaD,
		LambdaDMaxDelay:   networkInfo.KpConfigLambdaDMaxDelay,
		LambdaM:           networkInfo.KpConfigLambdaM,
		LambdaMMaxDelay:   networkInfo.KpConfigLambdaMMaxDelay,
		LambdaGMaxDelay:   networkInfo.KpConfigLambdaGMaxDelay,
	}

	s := &katzenpost{}

	s.ratchetNIKEScheme = *ratchetNike

	s.wireKEMScheme = *wirekem
	if kemschemes.ByName(*wirekem) == nil {
		return errors.New("invalid wire KEM scheme")
	}

	s.baseDir = *baseDir
	s.outDir = *outDir
	s.binPrefix = *binPrefix
	s.binSuffix = *binSuffix
	s.basePort = uint16(*basePort)
	s.lastPort = s.basePort + 1
	s.addr = *addr
	s.addrBind = *addrBind
	s.logLevel = *logLevel
	s.transport = *transport
	s.metrics = *metrics
	s.debugConfigClient1 = &cConfig.Debug{
		DisableDecoyTraffic:         networkInfo.KpClientDebugDisableDecoyTraffic,
		SessionDialTimeout:          networkInfo.KpClientDebugSessionDialTimeout,
		InitialMaxPKIRetrievalDelay: networkInfo.KpClientDebugInitialMaxPKIDelay,
		PollingInterval:             networkInfo.KpClientDebugPollingInterval,
	}
	s.debugConfigClient2 = &cConfig2.Debug{
		DisableDecoyTraffic:         networkInfo.KpClientDebugDisableDecoyTraffic,
		SessionDialTimeout:          networkInfo.KpClientDebugSessionDialTimeout,
		InitialMaxPKIRetrievalDelay: networkInfo.KpClientDebugInitialMaxPKIDelay,
		PollingInterval:             networkInfo.KpClientDebugPollingInterval,
		EnableTimeSync:              networkInfo.KpClientDebugEnableTimeSync,
	}
	s.debugConfigServer = &sConfig.Debug{
		ConnectTimeout:               networkInfo.KpDebugConnectTimeout,
		DecoySlack:                   networkInfo.KpDebugDecoySlack,
		DisableRateLimit:             networkInfo.KpDebugDisableRateLimit,
		GatewayDelay:                 networkInfo.KpDebugGatewayDelay,
		GenerateOnly:                 networkInfo.KpDebugGenerateOnly,
		HandshakeTimeout:             networkInfo.KpDebugHandshakeTimeout,
		KaetzchenDelay:               networkInfo.KpDebugKaetzchenDelay,
		NumGatewayWorkers:            networkInfo.KpDebugNumGatewayWorkers,
		NumKaetzchenWorkers:          networkInfo.KpDebugNumKaetzchenWorkers,
		NumServiceWorkers:            networkInfo.KpDebugNumServiceWorkers,
		NumSphinxWorkers:             networkInfo.KpDebugNumSphinxWorkers,
		ReauthInterval:               networkInfo.KpDebugReauthInterval,
		SchedulerExternalMemoryQueue: networkInfo.KpDebugSchedulerExternalMemoryQueue,
		SchedulerMaxBurst:            networkInfo.KpDebugSchedulerMaxBurst,
		SchedulerQueueSize:           networkInfo.KpDebugSchedulerQueueSize,
		SchedulerSlack:               networkInfo.KpDebugSchedulerSlack,
		SendDecoyTraffic:             networkInfo.KpDebugSendDecoyTraffic,
		SendSlack:                    networkInfo.KpDebugSendSlack,
		ServiceDelay:                 networkInfo.KpDebugServiceDelay,
		UnwrapDelay:                  networkInfo.KpDebugUnwrapDelay,
	}

	nrHops := *nrLayers + 2

	if *nike != "" {
		nikeScheme := schemes.ByName(*nike)
		if nikeScheme == nil {
			return errors.New(fmt.Sprintf("failed to resolve nike scheme %s", *nike))
		}
		s.sphinxGeometry = geo.GeometryFromUserForwardPayloadLength(
			nikeScheme,
			*UserForwardPayloadLength,
			true,
			nrHops,
		)
	}
	if *kem != "" {
		kemScheme := kemschemes.ByName(*kem)
		if kemScheme == nil {
			return errors.New(fmt.Sprintf("failed to resolve kem scheme %s", *kem))
		}
		s.sphinxGeometry = geo.KEMGeometryFromUserForwardPayloadLength(
			kemScheme,
			*UserForwardPayloadLength,
			true,
			nrHops,
		)
	}
	if *pkiSignatureScheme != "" {
		signScheme := signSchemes.ByName(*pkiSignatureScheme)
		if signScheme == nil {
			return errors.New(fmt.Sprintf("failed to resolve pki signature scheme %s", *pkiSignatureScheme))
		}
		s.pkiSignatureScheme = signScheme
	}

	os.Mkdir(s.outDir, 0700)

	if *voting {
		// Generate the voting authority configurations
		authIdentifier := fmt.Sprintf("%s-auth", *identifier)
		err := s.genVotingAuthoritiesCfg(authIdentifier, *nrVoting, parameters, *nrLayers, *wirekem)
		if err != nil {
			return errors.New(fmt.Sprintf("getVotingAuthoritiesCfg failed: %s", err))
		}
	}

	// Generate the gateway configs.
	for i := 0; i < *nrGateways; i++ {
		if err = s.genNodeConfig(*identifier, true, false, *voting); err != nil {
			return errors.New(fmt.Sprintf("Failed to generate provider config: %v", err))
		}
	}
	// Generate the service node configs.
	for i := 0; i < *nrServiceNodes; i++ {
		if err = s.genNodeConfig(*identifier, false, true, *voting); err != nil {
			return errors.New(fmt.Sprintf("Failed to generate provider config: %v", err))
		}
	}

	// Generate the mix node configs.
	for i := 0; i < *nrNodes; i++ {
		if err = s.genNodeConfig(*identifier, false, false, *voting); err != nil {
			return errors.New(fmt.Sprintf("Failed to generate node config: %v", err))
		}
	}
	// Generate the authority config
	if *voting {
		gateways, serviceNodes, mixes, err := s.genAuthorizedNodes()
		if err != nil {
			panic(err)
		}
		for _, vCfg := range s.votingAuthConfigs {
			vCfg.Mixes = mixes
			vCfg.GatewayNodes = gateways
			vCfg.ServiceNodes = serviceNodes
			if *omitTopology == false {
				vCfg.Topology = new(vConfig.Topology)
				vCfg.Topology.Layers = make([]vConfig.Layer, 0)
				for i := 0; i < *nrLayers; i++ {
					vCfg.Topology.Layers = append(vCfg.Topology.Layers, *new(vConfig.Layer))
					vCfg.Topology.Layers[i].Nodes = make([]vConfig.Node, 0)
				}
				for j := range mixes {
					layer := j % *nrLayers
					vCfg.Topology.Layers[layer].Nodes = append(vCfg.Topology.Layers[layer].Nodes, *mixes[j])
				}
			}
		}
		for _, vCfg := range s.votingAuthConfigs {
			if err := saveCfg(vCfg, *outDir); err != nil {
				return errors.New(fmt.Sprintf("Failed to saveCfg of authority with %s", err))
			}
		}
	}
	// write the mixes keys and configs to disk
	for _, v := range s.nodeConfigs {
		if err := saveCfg(v, *outDir); err != nil {
			return errors.New(fmt.Sprintf("saveCfg failure: %s", err))
		}
	}

	// Also generate a client config for the configured gateway.
	// TODO: client consumers get gateways from appchain.

	if *cfgType == "client1" || *cfgType == "gateway" {
		err = s.genClientCfg()
		if err != nil {
			return err
		}
	}

	if *cfgType == "client2" || *cfgType == "gateway" {
		err = s.genClient2Cfg()
		if err != nil {
			return err
		}
	}

	return nil
}

func identifier(cfg interface{}) string {
	switch cfg.(type) {
	case *cConfig.Config:
		return "client"
	case *cConfig2.Config:
		return "client2"
	case *sConfig.Config:
		return cfg.(*sConfig.Config).Server.Identifier
	case *vConfig.Config:
		return cfg.(*vConfig.Config).Server.Identifier
	default:
		log.Fatalf("identifier() passed unexpected type")
		return ""
	}
}

func toml_name(cfg interface{}) string {
	switch cfg.(type) {
	case *cConfig.Config:
		return "client"
	case *cConfig2.Config:
		return "client"
	case *sConfig.Config:
		return "katzenpost"
	case *vConfig.Config:
		return "authority"
	default:
		log.Fatalf("toml_name() passed unexpected type")
		return ""
	}
}

func saveCfg(cfg interface{}, outDir string) error {
	fileName := filepath.Join(outDir, identifier(cfg), fmt.Sprintf("%s.toml", toml_name(cfg)))
	log.Printf("writing %s", fileName)
	f, err := os.Create(fileName)
	if err != nil {
		return fmt.Errorf("os.Create(%s) failed: %s", fileName, err)
	}
	defer f.Close()

	// Serialize the descriptor.
	enc := toml.NewEncoder(f)
	return enc.Encode(cfg)
}

func saveFileContents(filename string, contents string) error {
	log.Printf("writing %s", filename)
	f, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("os.Create(%s) failed: %s", filename, err)
	}
	defer f.Close()
	if _, err := f.WriteString(contents); err != nil {
		return fmt.Errorf("f.WriteString() failed: %s", err)
	}
	return nil
}

func cfgIdKey(cfg interface{}, outDir string) sign.PublicKey {
	var priv, public string
	var pkiSignatureScheme string
	switch cfg.(type) {
	case *sConfig.Config:
		priv = filepath.Join(outDir, cfg.(*sConfig.Config).Server.Identifier, "identity.private.pem")
		public = filepath.Join(outDir, cfg.(*sConfig.Config).Server.Identifier, "identity.public.pem")
		pkiSignatureScheme = cfg.(*sConfig.Config).Server.PKISignatureScheme
	case *vConfig.Config:
		priv = filepath.Join(outDir, cfg.(*vConfig.Config).Server.Identifier, "identity.private.pem")
		public = filepath.Join(outDir, cfg.(*vConfig.Config).Server.Identifier, "identity.public.pem")
		pkiSignatureScheme = cfg.(*vConfig.Config).Server.PKISignatureScheme
	default:
		panic("wrong type")
	}

	scheme := signSchemes.ByName(pkiSignatureScheme)
	if scheme == nil {
		panic("invalid PKI signature scheme " + pkiSignatureScheme)
	}

	idPubKey, err := signpem.FromPublicPEMFile(public, scheme)
	if err == nil {
		return idPubKey
	}
	idPubKey, idKey, err := scheme.GenerateKey()
	log.Printf("writing %s", priv)
	signpem.PrivateKeyToFile(priv, idKey)
	log.Printf("writing %s", public)
	signpem.PublicKeyToFile(public, idPubKey)
	return idPubKey
}

func cfgLinkKey(cfg interface{}, outDir string, kemScheme string) kem.PublicKey {
	var linkpriv string
	var linkpublic string

	switch cfg.(type) {
	case *sConfig.Config:
		linkpriv = filepath.Join(outDir, cfg.(*sConfig.Config).Server.Identifier, "link.private.pem")
		linkpublic = filepath.Join(outDir, cfg.(*sConfig.Config).Server.Identifier, "link.public.pem")
	case *vConfig.Config:
		linkpriv = filepath.Join(outDir, cfg.(*vConfig.Config).Server.Identifier, "link.private.pem")
		linkpublic = filepath.Join(outDir, cfg.(*vConfig.Config).Server.Identifier, "link.public.pem")
	default:
		panic("wrong type")
	}

	linkPubKey, linkPrivKey, err := kemschemes.ByName(kemScheme).GenerateKeyPair()
	if err != nil {
		panic(err)
	}

	log.Printf("writing %s", linkpriv)
	err = kempem.PrivateKeyToFile(linkpriv, linkPrivKey)
	if err != nil {
		panic(err)
	}
	log.Printf("writing %s", linkpublic)
	err = kempem.PublicKeyToFile(linkpublic, linkPubKey)
	if err != nil {
		panic(err)
	}
	return linkPubKey
}
