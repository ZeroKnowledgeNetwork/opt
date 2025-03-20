// related: katzenpost:/authority/cmd/voting/main.go
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/carlmjohnson/versioninfo"

	"github.com/ZeroKnowledgeNetwork/opt/pki/config"
	"github.com/katzenpost/katzenpost/core/compat"
)

func main() {
	cfgFile := flag.String("f", "katzenpost-authority.toml", "Path to the authority config file.")
	genOnly := flag.Bool("g", false, "Generate the keys and exit immediately.")
	version := flag.Bool("v", false, "Get version info.")

	flag.Parse()

	if *version {
		fmt.Printf("version is %s\n", versioninfo.Short())
		return
	}

	// Set the umask to something "paranoid".
	compat.Umask(0077)

	cfg, err := config.LoadFile(*cfgFile, *genOnly)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config file '%v': %v\n", *cfgFile, err)
		os.Exit(-1)
	}

	// Setup the signal handling.
	ch := make(chan os.Signal)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)

	rotateCh := make(chan os.Signal)
	signal.Notify(rotateCh, syscall.SIGHUP)

	// Start up the authority.
	svr, err := New(cfg)
	if err != nil {
		if err == ErrGenerateOnly {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "Failed to spawn authority instance: %v\n", err)
		os.Exit(-1)
	}
	defer svr.Shutdown()

	// Halt the authority gracefully on SIGINT/SIGTERM.
	go func() {
		<-ch
		svr.Shutdown()
	}()

	// Rotate server logs upon SIGHUP.
	go func() {
		<-rotateCh
		svr.RotateLog()
	}()

	// Wait for the authority to explode or be terminated.
	svr.Wait()
}
