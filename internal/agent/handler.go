package agent

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	mockengine "github.com/project-mockingo/mockingo-agent/internal/mock"
	"github.com/project-mockingo/mockingo-agent/tunnelprotocol"
)

type Route string

const (
	RouteForward Route = "FORWARD"
	RouteMock    Route = "MOCK"
)

type HandlerResponse struct {
	Status  int
	Headers http.Header
	Body    []byte
	Route   Route
}

// RequestHandler is the agent-side seam between tunnel transport and request
// execution. A future recorder can observe forwarded request/response traffic
// at this boundary without changing the tunnel protocol.
type RequestHandler interface {
	Handle(*http.Request) (HandlerResponse, error)
}

type HandlerError struct {
	Code string
	Err  error
}

func (e *HandlerError) Error() string { return e.Err.Error() }
func (e *HandlerError) Unwrap() error { return e.Err }

type LocalForwarder struct {
	client *http.Client
}

func NewLocalForwarder(requestTimeout time.Duration) *LocalForwarder {
	return &LocalForwarder{client: &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func (f *LocalForwarder) Handle(request *http.Request) (HandlerResponse, error) {
	response, err := f.client.Do(request)
	if err != nil {
		code := tunnelprotocol.ErrorCodeLocalUnreachable
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(request.Context().Err(), context.DeadlineExceeded) {
			code = tunnelprotocol.ErrorCodeTimeout
		}
		return HandlerResponse{}, &HandlerError{Code: code, Err: err}
	}
	defer response.Body.Close()
	body, err := tunnelprotocol.ReadBody(response.Body)
	if err != nil {
		code := tunnelprotocol.ErrorCodeLocalResponseError
		if errors.Is(err, tunnelprotocol.ErrBodyTooLarge) {
			code = tunnelprotocol.ErrorCodeResponseTooLarge
		}
		return HandlerResponse{}, &HandlerError{Code: code, Err: err}
	}
	return HandlerResponse{Status: response.StatusCode, Headers: response.Header, Body: body, Route: RouteForward}, nil
}

type mockResponder interface {
	Respond(*http.Request, *mockengine.MockDefinition) (HandlerResponse, error)
}

type staticMockResponder struct{}

func (staticMockResponder) Respond(request *http.Request, definition *mockengine.MockDefinition) (HandlerResponse, error) {
	rendered, err := mockengine.Render(request.Context(), request.Method, definition, nil)
	if err != nil {
		return HandlerResponse{}, err
	}
	return HandlerResponse{Status: rendered.Status, Headers: rendered.Headers, Body: rendered.Body, Route: RouteMock}, nil
}

type HybridHandler struct {
	matcher   mockengine.Engine
	responder mockResponder
	forward   RequestHandler
	onError   func(error)
}

func NewHybridHandler(matcher mockengine.Engine, forward RequestHandler, onError func(error)) *HybridHandler {
	return &HybridHandler{matcher: matcher, responder: staticMockResponder{}, forward: forward, onError: onError}
}

func (h *HybridHandler) Handle(request *http.Request) (HandlerResponse, error) {
	definition, matched := h.matcher.Match(request)
	if !matched {
		return h.forward.Handle(request)
	}
	response, err := h.responder.Respond(request, definition)
	if err == nil {
		return response, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return HandlerResponse{}, &HandlerError{Code: tunnelprotocol.ErrorCodeTimeout, Err: err}
	}
	if h.onError != nil {
		h.onError(err)
	}
	body := []byte(`{"code":"internal_mock_error","message":"Mock response could not be rendered."}` + "\n")
	return HandlerResponse{
		Status: http.StatusInternalServerError,
		Headers: http.Header{
			"Content-Type":   {"application/json"},
			"Content-Length": {strconv.Itoa(len(body))},
		},
		Body: body, Route: RouteMock,
	}, nil
}
