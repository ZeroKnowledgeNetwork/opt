package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
	"gopkg.in/op/go-logging.v1"

	"github.com/katzenpost/katzenpost/core/log"
	"github.com/katzenpost/katzenpost/server/cborplugin"

	"github.com/ZeroKnowledgeNetwork/opt/common"
	"github.com/ZeroKnowledgeNetwork/opt/server_plugins/cbor_plugins/http_proxy"
)

// Note: UserForwardPayloadLength should match the same value passed to genconfig.
// MaxPayloadSize is set here as an approximation while a better solution is developed to inform the plugin of the Sphinx Geometry.
const UserForwardPayloadLength = 30000
const MaxPayloadSize = UserForwardPayloadLength - 20

func main() {
	var logLevel string
	var logDir string
	var destURL string
	var configPath string

	flag.StringVar(&logDir, "log_dir", "", "logging directory")
	flag.StringVar(&logLevel, "log_level", "DEBUG", "logging level could be set to: DEBUG, INFO, NOTICE, WARNING, ERROR, CRITICAL")
	flag.StringVar(&destURL, "dest_url", "", "destination URL for reverse proxying to")
	flag.StringVar(&configPath, "config", "", "file path to the TOML configuration file")

	flag.Parse()

	if configPath == "" {
		panic("config MUST be specified!")
	}

	cfg, err := http_proxy.LoadFile(configPath)
	if err != nil {
		panic(err)
	}

	// Ensure that the log directory exists.
	s, err := os.Stat(logDir)
	if os.IsNotExist(err) {
		fmt.Printf("Log directory '%s' doesn't exist.", logDir)
		os.Exit(1)
	}
	if !s.IsDir() {
		fmt.Println("Log directory must actually be a directory.")
		os.Exit(1)
	}

	// Log to a file.
	logFile := path.Join(logDir, fmt.Sprintf("http_proxy.%d.log", os.Getpid()))
	logBackend, err := log.New(logFile, logLevel, false)
	if err != nil {
		panic(err)
	}
	serverLog := logBackend.GetLogger("http_proxy")

	// start service
	tmpDir, err := os.MkdirTemp("", "http_proxy")
	if err != nil {
		panic(err)
	}
	socketFile := filepath.Join(tmpDir, fmt.Sprintf("%d.http_proxy.socket", os.Getpid()))

	var server *cborplugin.Server

	h := New(cfg, serverLog)
	server = cborplugin.NewServer(serverLog, socketFile, new(cborplugin.RequestFactory), h)

	// emit socketFile to stdout, because this tells the mix server where to connect
	fmt.Printf("%s\n", socketFile)

	server.Accept()
	server.Wait()

	os.Remove(socketFile)
}

func validateRequestURL(uri string) error {
	if string(uri)[0] != '/' {
		return errors.New("uri must start with a slash")
	}
	return nil
}

type proxyRequestHandler struct {
	cfg *http_proxy.Config
	log *logging.Logger

	write func(cborplugin.Command)
}

func New(cfg *http_proxy.Config, log *logging.Logger) *proxyRequestHandler {
	return &proxyRequestHandler{
		cfg: cfg,
		log: log,
	}
}

// OnCommand processes a request and returns a response
func (s *proxyRequestHandler) OnCommand(cmd cborplugin.Command) error {
	s.log.Info("OnCommand begin")
	defer s.log.Info("OnCommand end")

	switch r := cmd.(type) {
	case *cborplugin.Request:
		if r.SURB == nil {
			errMsg := "no SURB, cannot reply"
			s.log.Error(errMsg)
			return s.sendError(r.ID, r.SURB, errMsg)
		}

		// the padding bytes were not stripped because
		// without parsing the start of Payload we wont
		// know how long it is, so we will use a streaming
		// decoder and simply return the first cbor object
		// and then discard the decoder and buffer
		req := &http_proxy.Request{}
		dec := cbor.NewDecoder(bytes.NewReader(r.Payload))
		err := dec.Decode(req)
		if err != nil {
			s.log.Errorf("dec.Decode(req) failed: %s", err)
			return s.sendError(r.ID, r.SURB, "Error decoding request")
		}

		s.log.Debugf("Raw request payload: %s", req.Payload)

		requestPayload, err := common.DecompressData(req.Payload)
		if err != nil {
			s.log.Errorf("common.DecompressData failed: %s", err)
			return s.sendError(r.ID, r.SURB, "Failed to decompress request")
		}
		request, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(requestPayload)))
		if err != nil {
			s.log.Errorf("http.ReadRequest failed: %s", err)
			return s.sendError(r.ID, r.SURB, "Failed to read HTTP request")
		}

		s.log.Debugf("Request URL Path: %s", request.URL.Path)
		s.log.Debugf("Request payload: %s", requestPayload)

		err = validateRequestURL(request.URL.Path)
		if err != nil {
			s.log.Errorf("validateRequestURL failed: %s", err)
			return s.sendError(r.ID, r.SURB, fmt.Sprintf("validateRequestURL failed: %s", err.Error()))
		}

		if match, _ := regexp.MatchString("^/_/probe.*", request.URL.Path); match {
			segments := strings.Split(request.URL.Path, "/")
			if len(segments) >= 4 {
				responseDelay, err := strconv.Atoi(segments[3])
				if err == nil && responseDelay > 0 {
					s.log.Infof("probe: delaying response for %ds", responseDelay)
					time.Sleep(time.Duration(responseDelay) * time.Second)
				}
			}
			go s.write(&cborplugin.Response{ID: r.ID, SURB: r.SURB, Payload: nil})
			return nil
		}

		uri := request.URL.Path
		uri = uri[1:]
		target, ok := s.cfg.Networks[uri]
		if !ok {
			s.log.Error("URI not matched with config Networks")
			return s.sendError(r.ID, r.SURB, "URI not configured in networks")
		}

		newRequest, err := http.NewRequest(request.Method, target, request.Body)
		if err != nil {
			s.log.Errorf("http.NewRequest failed: %s", err)
			return s.sendError(r.ID, r.SURB, "Failed to create HTTP request")
		}

		newRequest.Header.Set("Content-Type", request.Header.Get("Content-Type"))
		newRequest.Header.Set("Content-Length", request.Header.Get("Content-Length"))

		resp, err := http.DefaultClient.Do(newRequest)
		if err != nil {
			s.log.Errorf("http.DefaultClient.Do failed: %s", err)
			return s.sendError(r.ID, r.SURB, "HTTP request failed")
		}
		defer resp.Body.Close()

		resp.Header["Host"] = []string{request.URL.Host}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			s.log.Errorf("io.ReadAll(resp.Body) failed: %s", err)
			return s.sendError(r.ID, r.SURB, fmt.Sprintf("io.ReadAll(resp.Body) failed: %s", err.Error()))
		}

		s.log.Debugf("Reply payload: %s", body)

		var response *http_proxy.Response
		responsePayload, err := common.CompressData(body)
		if err != nil {
			s.log.Errorf("common.CompressData failed: %s", err)
			return s.sendError(r.ID, r.SURB, "Failed to compress response")
		}
		if len(responsePayload) > MaxPayloadSize {
			s.log.Error("HTTP response body exceeds max Sphinx payload")
			return s.sendError(r.ID, r.SURB, "HTTP response is too big")
		} else {
			response = &http_proxy.Response{
				Payload: responsePayload,
			}
		}

		payload, err := cbor.Marshal(response)
		if err != nil {
			s.log.Errorf("cbor.Marshal(response) failed: %s", err)
			return s.sendError(r.ID, r.SURB, "Error marshaling response")
		}

		go s.write(&cborplugin.Response{ID: r.ID, SURB: r.SURB, Payload: payload})
		return nil
	default:
		errMsg := "invalid Command type"
		s.log.Error(errMsg)
		return errors.New(errMsg)
	}
}

func (s *proxyRequestHandler) sendError(id uint64, surb []byte, msg string) error {
	payload, _ := cbor.Marshal(&http_proxy.Response{Error: msg})
	go s.write(&cborplugin.Response{ID: id, SURB: surb, Payload: payload})
	return errors.New(msg)
}

// RegisterConsumer is required by our plugin interface
func (s *proxyRequestHandler) RegisterConsumer(svr *cborplugin.Server) {
	s.write = svr.Write
}
