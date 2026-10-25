package model

import "context"

const MaxEmbeddingDimensions = 16384

// EmbeddingRequest uses the same admitted-attempt and usage ledger as model
// generation. InputTokenEstimate is an estimate, not a billing guarantee.
type EmbeddingRequest struct {
	Model              string
	Input              []string
	Dimensions         int
	InputTokenEstimate int
	Metadata           map[string]string
}
type EmbeddingResponse struct {
	Vectors [][]float64
	Usage   Usage
}
type Embedder interface {
	Embed(context.Context, EmbeddingRequest) (EmbeddingResponse, error)
}
type governedEmbedder struct {
	client   Embedder
	governor *Governor
}

func GovernEmbedder(client Embedder, governor *Governor) Embedder {
	if client == nil {
		return nil
	}
	if _, ok := client.(*governedEmbedder); ok {
		return client
	}
	if governor == nil {
		governor = processGovernor
	}
	return &governedEmbedder{client, governor}
}
func (c *governedEmbedder) Embed(ctx context.Context, request EmbeddingRequest) (response EmbeddingResponse, err error) {
	ctx = context.WithValue(ctx, admissionGovernorKey{}, c.governor)
	spec := Request{Model: request.Model, Operation: "embedding", InputTokenEstimate: request.InputTokenEstimate, Metadata: request.Metadata}
	err = dispatchUsage(ctx, spec, func() (Usage, error) {
		var callErr error
		response, callErr = c.client.Embed(ctx, request)
		return response.Usage, callErr
	})
	return response, err
}
