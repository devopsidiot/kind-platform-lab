// Package main implements the composition function backing XAppEnvironment.
package main

import (
	"flag"
	"log"
	"os"

	function "github.com/crossplane/function-sdk-go"
)

// envOr returns the value of the environment variable, or fallback if unset.
//
// Crossplane configures a function through the environment rather than
// arguments, so every flag below has to default from it.
func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func main() {
	var (
		address = flag.String("address", envOr("FUNCTION_ADDRESS", ":9443"),
			"address the gRPC server listens on")
		// Crossplane mounts the serving certificates here and passes the path
		// via TLS_SERVER_CERTS_DIR. Without it the SDK refuses to serve.
		certsDir = flag.String("tls-certs-dir", os.Getenv("TLS_SERVER_CERTS_DIR"),
			"directory containing the TLS certificates used to serve gRPC")
		debug = flag.Bool("debug", os.Getenv("DEBUG") != "",
			"emit debug logs")
		insecure = flag.Bool("insecure", false,
			"serve gRPC without TLS; for local testing only")
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
