package providers

import (
	"bytes"
	"context"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Generate validates structured completions before publishing any content. Free
// text retains incremental streaming; structured consumers receive one complete
// validated value. Both paths retain the same transport, retries and accounting.
func (c *Client) Generate(ctx context.Context, request model.Request, sink model.EventSink) (model.Response, error) {
	if request.OutputFormat == nil {
		return c.generate(ctx, request, sink)
	}
	schema, err := compileOutput(request)
	if err != nil {
		return model.Response{}, err
	}
	response, err := c.generate(ctx, request, nil)
	if err == nil && request.OutputFormat.Mode != model.OutputValidated && c != nil && c.Host != nil && response.OutputMode != model.OutputNative {
		err = &model.Error{Kind: model.ErrorUnsupported, Message: "remote model did not acknowledge native structured output support"}
	}
	if err == nil {
		if response.FinishReason != model.FinishStop || len(response.Message.ToolCalls()) != 0 {
			kind := model.ErrorProtocol
			if response.FinishReason == model.FinishContent {
				kind = model.ErrorContentPolicy
			}
			err = &model.Error{Kind: kind, Message: "structured output did not complete without tools"}
		} else {
			value, decodeErr := jsonschema.UnmarshalJSON(bytes.NewBufferString(response.Message.Text()))
			if decodeErr != nil || schema.Validate(value) != nil {
				// Validator diagnostics can contain user content and schema enum values.
				err = &model.Error{Kind: model.ErrorProtocol, Message: "structured output does not match its JSON schema", Cause: model.ErrOutputValidation}
			}
		}
	}
	if err != nil {
		return model.Response{ID: response.ID, FinishReason: response.FinishReason, Usage: response.Usage}, err
	}
	response.OutputMode = request.OutputFormat.Mode
	if response.OutputMode == "" {
		response.OutputMode = model.OutputNative
	}
	if sink != nil {
		for _, event := range []model.StreamEvent{
			{Type: model.StreamTextDelta, Text: response.Message.Text(), Phase: "final_answer"},
			{Type: model.StreamCompleted, ResponseID: response.ID, Usage: &response.Usage},
		} {
			if err := sink(event); err != nil {
				return response, err
			}
		}
	}
	return response, nil
}

func compileOutput(request model.Request) (*jsonschema.Schema, error) {
	format := request.OutputFormat
	invalid := func() (*jsonschema.Schema, error) {
		return nil, &model.Error{Kind: model.ErrorInvalid, Message: "invalid self-contained structured output contract"}
	}
	if request.Operation != "" || len(request.Tools) != 0 || format.Name == "" || len(format.Name) > 64 {
		return invalid()
	}
	for _, c := range format.Name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return invalid()
		}
	}
	if format.Mode != "" && format.Mode != model.OutputNative && format.Mode != model.OutputValidated {
		return invalid()
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(format.Schema))
	if err != nil {
		return invalid()
	}
	if _, ok := value.(map[string]any); !ok {
		return invalid()
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(localOutputSchema{})
	const resource = "https://kepler.invalid/output.json"
	if compiler.AddResource(resource, value) != nil {
		return invalid()
	}
	schema, err := compiler.Compile(resource)
	if err != nil {
		return invalid()
	}
	return schema, nil
}

type localOutputSchema struct{}

func (localOutputSchema) Load(string) (any, error) {
	return nil, fmt.Errorf("output schemas must contain their own references")
}
