package cli

import (
	"context"

	mockengine "github.com/project-mockingo/mockingo-agent/internal/mock"
	openapiadapter "github.com/project-mockingo/mockingo-agent/internal/mock/openapi"
	"github.com/project-mockingo/mockingo-agent/internal/mock/wiremock"
)

type loadedMockSource struct {
	Engine     *mockengine.CompiledEngine
	Source     string
	CountLabel string
	Count      int
}

func loadMockSource(ctx context.Context, wireMockPath, openAPIPath string, warning func(string)) (loadedMockSource, error) {
	var (
		definitions []mockengine.MockDefinition
		err         error
		result      loadedMockSource
	)
	if wireMockPath != "" {
		result.Source = "WireMock"
		result.CountLabel = "Mappings"
		definitions, err = wiremock.Load(wireMockPath)
	} else {
		result.Source = "OpenAPI"
		result.CountLabel = "Operations"
		definitions, err = openapiadapter.Load(ctx, openAPIPath, warning)
	}
	if err != nil {
		return loadedMockSource{}, err
	}
	result.Count = len(definitions)
	result.Engine = mockengine.Compile(definitions)
	return result, nil
}
