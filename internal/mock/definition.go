package mock

import (
	"net/http"
	"time"
)

type HTTPMethod string

const MethodAny HTTPMethod = "ANY"

type PathMatcherType string

const (
	PathExactURL  PathMatcherType = "exact_url"
	PathExactPath PathMatcherType = "exact_path"
	PathTemplate  PathMatcherType = "path_template"
)

type PathMatcher struct {
	Type  PathMatcherType
	Value string
}

type RequestMatcher struct {
	Method HTTPMethod
	Path   PathMatcher
}

type ResponseDefinition struct {
	Status     int
	Headers    http.Header
	Body       []byte
	FixedDelay time.Duration
}

type SourceMetadata struct {
	Format string
	File   string
}

type MockDefinition struct {
	ID       string
	Name     string
	Priority int
	Request  RequestMatcher
	Response ResponseDefinition
	Source   SourceMetadata
}

const DefaultPriority = 5
