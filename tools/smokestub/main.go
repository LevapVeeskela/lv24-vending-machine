// Command smokestub is a local rehearsal double for the live smoke runbook.
//
// One loopback HTTP server answers the three provider roles used by
// docs/runbook-live-smoke.md:
//
//	POST /v1/systemone        decision-drex response
//	GET  /api/v3/klines       synthetic BTCUSDT candles
//	POST /bot<token>/sendMessage  Telegram sendMessage success
//	GET  /ctl?decision=&telegram=  switch the HTTP status of a role
//	GET  /ctl/stats           request counters by role (no tokens or texts)
//
// It uses only the standard library and never talks to Drex, Binance or
// Telegram. The command is a rehearsal tool, not a release artifact:
// tools/release does not build it.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

// defaultAddr is the loopback address documented in the runbook.
const defaultAddr = "127.0.0.1:28471"

func main() {
	addr := flag.String("addr", defaultAddr, "loopback listen address (127.0.0.1 or ::1)")
	flag.Parse()
	if err := run(*addr, time.Now); err != nil {
		fmt.Fprintf(os.Stderr, "smokestub: %v\n", err)
		os.Exit(1)
	}
}

// run validates the address, starts the server and blocks until it fails.
func run(addr string, now func() time.Time) error {
	if err := validateAddr(addr); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	logger := log.New(os.Stderr, "smokestub: ", log.LstdFlags)
	logger.Printf("listening on http://%s", ln.Addr())
	srv := &http.Server{
		Handler:           newStub(now, logger),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.Serve(ln)
}

// validateAddr keeps the rehearsal server on a loopback host: it answers
// provider requests without authentication and must not be exposed.
func validateAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid --addr %q: %w", addr, err)
	}
	if host != "127.0.0.1" && host != "::1" {
		return fmt.Errorf("--addr must be loopback (127.0.0.1 or ::1), got %q", host)
	}
	return nil
}
