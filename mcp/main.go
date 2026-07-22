// Package main implements a read-only MCP server that exposes this cluster's
// Crossplane state as tools over stdio. Claude Desktop spawns the binary as a
// child process and speaks JSON-RPC over stdin/stdout, so every tool inspects
// and none mutate, and all logging goes to stderr: anything written to stdout
// corrupts the JSON-RPC stream.
package main

import (
	"log"
	"os"

	"github.com/mark3labs/mcp-go/server"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	// Stderr explicitly: stdout belongs to the JSON-RPC stream.
	logger := log.New(os.Stderr, "mcp-server: ", log.LstdFlags)

	// Reuse the cluster's kubeconfig and current context; the server does not
	// manage its own credentials.
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		logger.Fatalf("load kubeconfig: %v", err)
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		logger.Fatalf("create dynamic client: %v", err)
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		logger.Fatalf("create kubernetes client: %v", err)
	}
	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		logger.Fatalf("create discovery client: %v", err)
	}

	insp := &inspector{
		client:    dyn,
		core:      core,
		resources: disc.ServerPreferredResources,
		log:       logger,
	}

	// WithRecovery turns a panic in a tool handler into a JSON-RPC error
	// response instead of killing the server.
	s := server.NewMCPServer("kind-platform-lab-inspector", "0.1.0",
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)
	s.AddTool(xrStatusTool(), insp.handleGetXRStatus)
	s.AddTool(compositionPipelineTool(), insp.handleGetCompositionPipeline)
	s.AddTool(policyViolationsTool(), insp.handleListPolicyViolations)
	s.AddTool(functionLogsTool(), insp.handleGetFunctionLogs)

	logger.Println("serving MCP over stdio")
	if err := server.ServeStdio(s); err != nil {
		logger.Fatalf("serve: %v", err)
	}
}
