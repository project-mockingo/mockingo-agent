package cli

import (
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/project-mockingo/mockingo-agent/tunnelprotocol"
)

type envFlags []string

func (f *envFlags) String() string { return strings.Join(*f, ",") }
func (f *envFlags) Set(value string) error {
	*f = append(*f, value)
	return nil
}

type ExposeOptions struct {
	Name                  string
	HTTPPort              int
	WireMock              string
	OpenAPI               string
	CWD                   string
	Environment           map[string]string
	StartupTimeout        time.Duration
	RequestTimeout        time.Duration
	Verbose               bool
	Command               []string
	APIURL                string
	ExpectedGatewayHost   string
	ProtocolVersion       int
	ReconnectEnabled      bool
	ReconnectInitialDelay time.Duration
	ReconnectMaxDelay     time.Duration
	AllowInsecureGateway  bool
	AllowFileCredentials  bool
}

type MockOptions struct {
	Name                  string
	WireMock              string
	OpenAPI               string
	RequestTimeout        time.Duration
	Verbose               bool
	APIURL                string
	ExpectedGatewayHost   string
	ProtocolVersion       int
	ReconnectEnabled      bool
	ReconnectInitialDelay time.Duration
	ReconnectMaxDelay     time.Duration
	AllowInsecureGateway  bool
	AllowFileCredentials  bool
}

func ParseMock(args []string) (MockOptions, error) {
	protocolVersion, err := envInt("MOCKINGO_TUNNEL_PROTOCOL_VERSION", tunnelprotocol.Version)
	if err != nil {
		return MockOptions{}, err
	}
	reconnectEnabled, err := envBool("MOCKINGO_RECONNECT_ENABLED", true)
	if err != nil {
		return MockOptions{}, err
	}
	allowInsecure, err := envBool("MOCKINGO_ALLOW_INSECURE_GATEWAY", false)
	if err != nil {
		return MockOptions{}, err
	}
	allowFile, err := envBool("MOCKINGO_ALLOW_FILE_CREDENTIALS", false)
	if err != nil {
		return MockOptions{}, err
	}
	initialDelay, err := envDuration("MOCKINGO_RECONNECT_INITIAL_DELAY", time.Second)
	if err != nil {
		return MockOptions{}, err
	}
	maxDelay, err := envDuration("MOCKINGO_RECONNECT_MAX_DELAY", 30*time.Second)
	if err != nil {
		return MockOptions{}, err
	}
	options := MockOptions{
		ProtocolVersion: protocolVersion, ReconnectEnabled: reconnectEnabled,
		AllowInsecureGateway: allowInsecure, AllowFileCredentials: allowFile,
		ReconnectInitialDelay: initialDelay, ReconnectMaxDelay: maxDelay,
	}
	set := flag.NewFlagSet("mock", flag.ContinueOnError)
	set.SetOutput(new(strings.Builder))
	set.StringVar(&options.Name, "name", "", "endpoint name")
	set.StringVar(&options.WireMock, "wiremock", "", "WireMock project directory or mapping JSON file")
	set.StringVar(&options.OpenAPI, "openapi", "", "OpenAPI YAML or JSON document")
	set.DurationVar(&options.RequestTimeout, "request-timeout", 60*time.Second, "request timeout")
	set.BoolVar(&options.Verbose, "verbose", false, "verbose diagnostics and request logs")
	set.StringVar(&options.APIURL, "api-url", envString("MOCKINGO_API_URL", ""), "Mockingo control-plane API URL")
	set.StringVar(&options.ExpectedGatewayHost, "expected-gateway-host", envString("MOCKINGO_EXPECTED_GATEWAY_HOST", "gateway.mockingo.com"), "trusted gateway hostname (comma-separated)")
	set.IntVar(&options.ProtocolVersion, "tunnel-protocol-version", options.ProtocolVersion, "tunnel protocol version")
	set.BoolVar(&options.ReconnectEnabled, "reconnect", options.ReconnectEnabled, "reconnect after connection loss")
	set.DurationVar(&options.ReconnectInitialDelay, "reconnect-initial-delay", options.ReconnectInitialDelay, "initial reconnect delay")
	set.DurationVar(&options.ReconnectMaxDelay, "reconnect-max-delay", options.ReconnectMaxDelay, "maximum reconnect delay")
	set.BoolVar(&options.AllowInsecureGateway, "allow-insecure-gateway", options.AllowInsecureGateway, "allow ws:// for an explicitly trusted loopback gateway")
	set.BoolVar(&options.AllowFileCredentials, "allow-insecure-storage", options.AllowFileCredentials, "allow owner-only fallback OAuth credential storage")
	if err := set.Parse(args); err != nil {
		return MockOptions{}, err
	}
	if len(set.Args()) != 0 {
		return MockOptions{}, errors.New("mock does not accept positional arguments")
	}
	if options.Name == "" {
		return MockOptions{}, errors.New("--name is required")
	}
	if (options.WireMock == "") == (options.OpenAPI == "") {
		return MockOptions{}, errors.New("exactly one of --wiremock or --openapi is required")
	}
	if options.RequestTimeout <= 0 {
		return MockOptions{}, errors.New("--request-timeout must be greater than zero")
	}
	if options.ProtocolVersion != tunnelprotocol.Version {
		return MockOptions{}, errors.New("--tunnel-protocol-version must be 1")
	}
	if options.ReconnectInitialDelay <= 0 || options.ReconnectMaxDelay < options.ReconnectInitialDelay {
		return MockOptions{}, errors.New("reconnect delays must be positive and maximum must not be less than initial")
	}
	if strings.TrimSpace(options.ExpectedGatewayHost) == "" {
		return MockOptions{}, errors.New("--expected-gateway-host is required")
	}
	return options, nil
}

func envBool(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(envString(name, ""))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return parsed, nil
}

func ParseEnvironment(values []string) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for _, value := range values {
		key, val, found := strings.Cut(value, "=")
		if !found || key == "" || strings.ContainsRune(key, '\x00') || strings.Contains(key, "=") {
			return nil, fmt.Errorf("invalid environment value %q; expected KEY=VALUE", value)
		}
		result[key] = val
	}
	return result, nil
}

func ParseExpose(args []string) (ExposeOptions, error) {
	var options ExposeOptions
	var env envFlags
	protocolVersion, err := envInt("MOCKINGO_TUNNEL_PROTOCOL_VERSION", tunnelprotocol.Version)
	if err != nil {
		return ExposeOptions{}, err
	}
	reconnectEnabled, err := envBool("MOCKINGO_RECONNECT_ENABLED", true)
	if err != nil {
		return ExposeOptions{}, err
	}
	allowInsecure, err := envBool("MOCKINGO_ALLOW_INSECURE_GATEWAY", false)
	if err != nil {
		return ExposeOptions{}, err
	}
	allowFile, err := envBool("MOCKINGO_ALLOW_FILE_CREDENTIALS", false)
	if err != nil {
		return ExposeOptions{}, err
	}
	initialDelay, err := envDuration("MOCKINGO_RECONNECT_INITIAL_DELAY", time.Second)
	if err != nil {
		return ExposeOptions{}, err
	}
	maxDelay, err := envDuration("MOCKINGO_RECONNECT_MAX_DELAY", 30*time.Second)
	if err != nil {
		return ExposeOptions{}, err
	}
	options.ProtocolVersion = protocolVersion
	options.ReconnectEnabled = reconnectEnabled
	options.AllowInsecureGateway = allowInsecure
	options.AllowFileCredentials = allowFile
	options.ReconnectInitialDelay = initialDelay
	options.ReconnectMaxDelay = maxDelay
	set := flag.NewFlagSet("expose", flag.ContinueOnError)
	set.SetOutput(new(strings.Builder))
	set.StringVar(&options.Name, "name", "", "tunnel name")
	set.IntVar(&options.HTTPPort, "http", 0, "local HTTP port")
	set.StringVar(&options.WireMock, "wiremock", "", "WireMock project directory or mapping JSON file")
	set.StringVar(&options.OpenAPI, "openapi", "", "OpenAPI YAML or JSON document")
	set.StringVar(&options.CWD, "cwd", "", "child working directory")
	set.Var(&env, "env", "child environment KEY=VALUE")
	set.DurationVar(&options.StartupTimeout, "startup-timeout", 60*time.Second, "startup timeout")
	set.DurationVar(&options.RequestTimeout, "request-timeout", 60*time.Second, "request timeout")
	set.BoolVar(&options.Verbose, "verbose", false, "verbose diagnostics")
	set.StringVar(&options.APIURL, "api-url", envString("MOCKINGO_API_URL", ""), "Mockingo control-plane API URL")
	set.StringVar(&options.ExpectedGatewayHost, "expected-gateway-host", envString("MOCKINGO_EXPECTED_GATEWAY_HOST", "gateway.mockingo.com"), "trusted gateway hostname (comma-separated)")
	set.IntVar(&options.ProtocolVersion, "tunnel-protocol-version", options.ProtocolVersion, "tunnel protocol version")
	set.BoolVar(&options.ReconnectEnabled, "reconnect", options.ReconnectEnabled, "reconnect after connection loss")
	set.DurationVar(&options.ReconnectInitialDelay, "reconnect-initial-delay", options.ReconnectInitialDelay, "initial reconnect delay")
	set.DurationVar(&options.ReconnectMaxDelay, "reconnect-max-delay", options.ReconnectMaxDelay, "maximum reconnect delay")
	set.BoolVar(&options.AllowInsecureGateway, "allow-insecure-gateway", options.AllowInsecureGateway, "allow ws:// for an explicitly trusted loopback gateway")
	set.BoolVar(&options.AllowFileCredentials, "allow-insecure-storage", options.AllowFileCredentials, "allow owner-only fallback OAuth credential storage")
	if err := set.Parse(args); err != nil {
		return ExposeOptions{}, err
	}
	if options.Name == "" {
		return ExposeOptions{}, errors.New("--name is required")
	}
	if options.HTTPPort < 1 || options.HTTPPort > 65535 {
		return ExposeOptions{}, errors.New("--http must be a port between 1 and 65535")
	}
	if options.WireMock != "" && options.OpenAPI != "" {
		return ExposeOptions{}, errors.New("--wiremock and --openapi are mutually exclusive")
	}
	if options.StartupTimeout <= 0 || options.RequestTimeout <= 0 {
		return ExposeOptions{}, errors.New("timeouts must be greater than zero")
	}
	if options.ProtocolVersion != 1 {
		return ExposeOptions{}, errors.New("--tunnel-protocol-version must be 1")
	}
	if options.ReconnectInitialDelay <= 0 || options.ReconnectMaxDelay < options.ReconnectInitialDelay {
		return ExposeOptions{}, errors.New("reconnect delays must be positive and maximum must not be less than initial")
	}
	if strings.TrimSpace(options.ExpectedGatewayHost) == "" {
		return ExposeOptions{}, errors.New("--expected-gateway-host is required")
	}
	parsedEnv, err := ParseEnvironment(env)
	if err != nil {
		return ExposeOptions{}, err
	}
	options.Environment = parsedEnv
	options.Command = append([]string(nil), set.Args()...)
	return options, nil
}

func formatCommand(parts []string) string {
	quoted := make([]string, len(parts))
	for i, part := range parts {
		if strings.ContainsAny(part, " \t\"'") {
			quoted[i] = strconv.Quote(part)
		} else {
			quoted[i] = part
		}
	}
	return strings.Join(quoted, " ")
}
