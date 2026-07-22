package main

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// pipelineStep is one step of a Composition's function pipeline.
type pipelineStep struct {
	Step        string `json:"step"`
	FunctionRef string `json:"functionRef"`
	// Input is the step's optional input object, passed through verbatim.
	Input any `json:"input,omitempty"`
}

// compositionPipeline is the structured result of get_composition_pipeline.
type compositionPipeline struct {
	Name             string         `json:"name"`
	CompositeTypeRef map[string]any `json:"compositeTypeRef"`
	Mode             string         `json:"mode"`
	Steps            []pipelineStep `json:"steps"`
}

func compositionPipelineTool() mcp.Tool {
	return mcp.NewTool("get_composition_pipeline",
		mcp.WithDescription("Get the function pipeline of a Crossplane Composition: "+
			"the ordered steps, the function each step calls and its input. "+
			"Returns structured JSON."),
		mcp.WithString("name", mcp.Required(),
			mcp.Description("Name of the Composition, e.g. xappenvironments.platform.devopsidiot.io")),
	)
}

func (i *inspector) handleGetCompositionPipeline(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := req.RequireString("name")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	lists, err := i.resources()
	if err != nil && len(lists) == 0 {
		return mcp.NewToolResultErrorf("discover API resources: %v", clusterError(err)), nil
	}

	pipeline, err := i.compositionPipeline(ctx, lists, name)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(pipeline)
}

// compositionPipeline fetches the named Composition and extracts its pipeline.
func (i *inspector) compositionPipeline(ctx context.Context, lists []*metav1.APIResourceList, name string) (*compositionPipeline, error) {
	matches := resolveKind(lists, "Composition", "")
	if len(matches) != 1 {
		return nil, fmt.Errorf("cannot resolve kind Composition to exactly one API resource (got %d matches)", len(matches))
	}

	comp, err := i.client.Resource(matches[0].gvr).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("composition %q not found in the cluster; "+
			"check the name against the installed Compositions", name)
	}
	if err != nil {
		return nil, fmt.Errorf("get composition %q: %w", name, clusterError(err))
	}

	typeRef, _, _ := unstructured.NestedMap(comp.Object, "spec", "compositeTypeRef")
	mode, _, _ := unstructured.NestedString(comp.Object, "spec", "mode")

	rawSteps, _, _ := unstructured.NestedSlice(comp.Object, "spec", "pipeline")
	steps := make([]pipelineStep, 0, len(rawSteps))
	for _, s := range rawSteps {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		step, _ := m["step"].(string)
		fnName, _, _ := unstructured.NestedString(m, "functionRef", "name")
		steps = append(steps, pipelineStep{
			Step:        step,
			FunctionRef: fnName,
			Input:       m["input"],
		})
	}

	return &compositionPipeline{
		Name:             comp.GetName(),
		CompositeTypeRef: typeRef,
		Mode:             mode,
		Steps:            steps,
	}, nil
}
