package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/project-mockingo/mockingo-agent/internal/apiclient"
	"github.com/project-mockingo/mockingo-agent/internal/dependencycapture"
	"github.com/project-mockingo/mockingo-agent/tunnelprotocol"
)

type dependencyRuntimeOptions struct {
	Name                  string
	ProxyPort             int
	PassthroughHosts      []string
	ExpectedGatewayHost   string
	ReconnectInitialDelay time.Duration
	ReconnectMaxDelay     time.Duration
	AllowInsecureGateway  bool
	Verbose               bool
	RequireInitialSession bool
}

type dependencyRuntime struct {
	cancel          context.CancelFunc
	proxy           *dependencycapture.Proxy
	proxyAddress    string
	certificatePath string
	behaviors       *dependencycapture.BehaviorStore
	proxyDone       chan struct{}
	wg              sync.WaitGroup
	closeOnce       sync.Once
	errMu           sync.Mutex
	proxyErr        error
}

func (a *App) startDependencyRuntime(
	parent context.Context,
	controlClient *apiclient.Client,
	options dependencyRuntimeOptions,
) (*dependencyRuntime, error) {
	ca, certificatePath, err := dependencycapture.LoadOrCreateCA("")
	if err != nil {
		return nil, fmt.Errorf("cannot prepare HTTPS inspection CA: %w", err)
	}
	validation := apiclient.TunnelSessionValidation{
		ExpectedGatewayHosts: strings.Split(options.ExpectedGatewayHost, ","),
		AllowInsecureLocal:   options.AllowInsecureGateway,
	}
	createSession := func(sessionCtx context.Context) (dependencycapture.CaptureSession, error) {
		response, createErr := controlClient.CreateDependencyCaptureSession(sessionCtx, options.Name, validation)
		if createErr != nil {
			return dependencycapture.CaptureSession{}, mapCaptureSessionError(options.Name, createErr)
		}
		return dependencycapture.CaptureSession{
			EndpointID: response.Endpoint.ID, EndpointName: response.Endpoint.Name,
			SessionID: response.Capture.SessionID, ConnectURL: response.Capture.ConnectURL,
			Ticket: response.Capture.Ticket,
		}, nil
	}
	var initial *dependencycapture.CaptureSession
	if options.RequireInitialSession {
		session, createErr := createSession(parent)
		if createErr != nil {
			return nil, createErr
		}
		initial = &session
	}

	behaviors := dependencycapture.NewBehaviorStore()
	uploader := dependencycapture.NewUploader(dependencycapture.UploaderConfig{
		InitialSession: initial, AcquireSession: createSession, Retryable: apiclient.IsRetryable,
		QueueSize: 100, ReconnectInitialDelay: options.ReconnectInitialDelay,
		ReconnectMaxDelay: options.ReconnectMaxDelay,
		OnState:           func(message string) { fmt.Fprintln(a.Stdout, message) },
		OnDrop: func() {
			fmt.Fprintln(a.Stderr, "Warning: dependency capture telemetry was dropped; proxy forwarding is unaffected.")
		},
		OnConfig: func(snapshot tunnelprotocol.DependencyBehaviorSnapshot) error {
			if replaceErr := behaviors.Replace(snapshot); replaceErr != nil {
				return replaceErr
			}
			for _, behavior := range snapshot.Behaviors {
				for _, passthrough := range options.PassthroughHosts {
					if strings.EqualFold(strings.TrimSuffix(behavior.Host, "."), strings.TrimSuffix(passthrough, ".")) {
						fmt.Fprintf(a.Stderr, "Warning: dependency replay %s is inactive because %s is configured for HTTPS passthrough.\n", behavior.ID, behavior.Host)
					}
				}
			}
			fmt.Fprintf(a.Stdout, "Dependency replay configuration loaded: %d active.\n", len(snapshot.Behaviors))
			return nil
		},
	})
	var verbose func(string, ...any)
	if options.Verbose {
		verbose = func(format string, values ...any) { fmt.Fprintf(a.Stderr, "debug: "+format+"\n", values...) }
	}
	proxy, err := dependencycapture.NewProxy(dependencycapture.ProxyConfig{
		Port: options.ProxyPort, CA: ca, PassthroughHosts: options.PassthroughHosts,
		Behaviors: behaviors, Emit: uploader.Enqueue, Verbose: verbose,
		OnCompleted: func(event tunnelprotocol.DependencyInteraction) {
			fmt.Fprintf(a.Stdout, "%s %s://%s%s %d %dms\n", event.Request.Method, event.Scheme, event.Host, event.Request.Path, event.Response.Status, event.DurationMS)
		},
	})
	if err != nil {
		return nil, err
	}
	listener, err := proxy.Listen()
	if err != nil {
		hint := "use --proxy-port to choose another port"
		if !options.RequireInitialSession {
			hint += " or --dependency-proxy=false to disable it"
		}
		return nil, fmt.Errorf("cannot start dependency proxy on 127.0.0.1:%d: the port may already be in use; %s: %w", options.ProxyPort, hint, err)
	}
	runCtx, cancel := context.WithCancel(parent)
	runtime := &dependencyRuntime{
		cancel: cancel, proxy: proxy, proxyAddress: listener.Addr().String(),
		certificatePath: certificatePath, behaviors: behaviors, proxyDone: make(chan struct{}),
	}
	runtime.wg.Add(2)
	go func() {
		defer runtime.wg.Done()
		serveErr := proxy.Serve(runCtx, listener)
		runtime.errMu.Lock()
		runtime.proxyErr = serveErr
		runtime.errMu.Unlock()
		close(runtime.proxyDone)
	}()
	go func() {
		defer runtime.wg.Done()
		uploadErr := uploader.Run(runCtx)
		if runCtx.Err() == nil {
			if uploadErr != nil {
				fmt.Fprintf(a.Stderr, "Warning: dependency capture upload stopped: %v. Proxy forwarding continues.\n", uploadErr)
			} else {
				fmt.Fprintln(a.Stderr, "Warning: dependency capture upload stopped. Proxy forwarding continues.")
			}
		}
	}()
	return runtime, nil
}

func (r *dependencyRuntime) printStarted(output io.Writer, endpointName string) {
	_, port, _ := net.SplitHostPort(r.proxyAddress)
	fmt.Fprintf(output, "\nDependency proxy started\n\nEndpoint       %s\nProxy          http://127.0.0.1:%s\nHTTPS          inspection enabled (HTTP/1.1)\nCA certificate %s\nReplays        %d active (waiting for cloud snapshot)\n\nConfigure your application manually to use this proxy.\nTrust the CA certificate in the application runtime for HTTPS.\n", endpointName, port, r.certificatePath, r.behaviors.Count())
}

func (r *dependencyRuntime) ProxyDone() <-chan struct{} { return r.proxyDone }

func (r *dependencyRuntime) ProxyError() error {
	r.errMu.Lock()
	defer r.errMu.Unlock()
	return r.proxyErr
}

func (r *dependencyRuntime) Close() {
	r.closeOnce.Do(func() {
		r.cancel()
		r.proxy.CloseIdleConnections()
		r.wg.Wait()
	})
}
