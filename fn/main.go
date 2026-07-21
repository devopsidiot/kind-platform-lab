// Package main implements the composition function backing XAppEnvironment.
package main

import (
	"flag"
	"log"

	function "github.com/crossplane/function-sdk-go"
)

func main() {
	var (
		address  = flag.String("address", ":9443", "address the gRPC server listens on")
		certsDir = flag.String("tls-certs-dir", "", "directory containing the TLS certificates used to serve gRPC")
		debug    = flag.Bool("debug", false, "emit debug logs")
		insecure = flag.Bool("insecure", false, "serve gRPC without TLS; for local testing only")
	)
	flag.Parse()

	logger, err := function.NewLogger(*debug)
	if err != nil {
		log.Fatalf("cannot create logger: %v", err)
	}

	if err := function.Serve(
		NewFunction(logger),
		function.Listen(function.DefaultNetwork, *address),
		function.MTLSCertificates(*certsDir),
		function.Insecure(*insecure),
	); err != nil {
		log.Fatalf("cannot serve function: %v", err)
	}
}
