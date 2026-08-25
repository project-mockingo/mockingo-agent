package cli

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"sort"
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
	DependencyProxy       bool
	ProxyPort             int
	PassthroughHosts      []string
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

type stringFlags []string

func (f *stringFlags) String() string { return strings.Join(*f, ",") }
func (f *stringFlags) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("value must not be empty")
	}
	*f = append(*f, value)
	return nil
}

type CaptureOptions struct {
	Name                  string
	ProxyPort             int
	PassthroughHosts      []string
	Verbose               bool
	APIURL                string
	ExpectedGatewayHost   string
	ReconnectInitialDelay time.Duration
	ReconnectMaxDelay     time.Duration
	AllowInsecureGateway  bool
	AllowFileCredentials  bool
}

func ParseCapture(args []string) (CaptureOptions, error) {
	var options CaptureOptions
	var passthrough stringFlags
	allowInsecure, err := envBool("MOCKINGO_ALLOW_INSECURE_GATEWAY", false)
	if err != nil {
		return options, err
	}
	allowFile, err := envBool("MOCKINGO_ALLOW_FILE_CREDENTIALS", false)
	if err != nil {
		return options, err
	}
	initialDelay, err := envDuration("MOCKINGO_RECONNECT_INITIAL_DELAY", time.Second)
	if err != nil {
		return options, err
	}
	maxDelay, err := envDuration("MOCKINGO_RECONNECT_MAX_DELAY", 30*time.Second)
	if err != nil {
		return options, err
	}
	options.AllowInsecureGateway = allowInsecure
	options.AllowFileCredentials = allowFile
	options.ReconnectInitialDelay = initialDelay
	options.ReconnectMaxDelay = maxDelay
	set := flag.NewFlagSet("capture", flag.ContinueOnError)
	set.SetOutput(new(strings.Builder))
	set.StringVar(&options.Name, "name", "", "endpoint name")
	set.IntVar(&options.ProxyPort, "proxy-port", 8899, "loopback dependency proxy port")
	set.Var(&passthrough, "passthrough-host", "exact HTTPS hostname to tunnel without inspection")
	set.BoolVar(&options.Verbose, "verbose", false, "verbose diagnostics")
	set.StringVar(&options.APIURL, "api-url", envString("MOCKINGO_API_URL", ""), "Mockingo control-plane API URL")
	set.StringVar(&options.ExpectedGatewayHost, "expected-gateway-host", envString("MOCKINGO_EXPECTED_GATEWAY_HOST", "gateway.mockingo.com"), "trusted gateway hostname (comma-separated)")
	set.DurationVar(&options.ReconnectInitialDelay, "reconnect-initial-delay", options.ReconnectInitialDelay, "initial reconnect delay")
	set.DurationVar(&options.ReconnectMaxDelay, "reconnect-max-delay", options.ReconnectMaxDelay, "maximum reconnect delay")
	set.BoolVar(&options.AllowInsecureGateway, "allow-insecure-gateway", options.AllowInsecureGateway, "allow ws:// for an explicitly trusted loopback gateway")
	set.BoolVar(&options.AllowFileCredentials, "allow-insecure-storage", options.AllowFileCredentials, "allow owner-only fallback OAuth credential storage")
	if err := set.Parse(args); err != nil {
		return CaptureOptions{}, err
	}
	if len(set.Args()) != 0 {
		return CaptureOptions{}, errors.New("capture does not launch an application; unexpected positional arguments")
	}
	if options.Name == "" {
		return CaptureOptions{}, errors.New("--name is required")
	}
	if options.ProxyPort < 1 || options.ProxyPort > 65535 {
		return CaptureOptions{}, errors.New("--proxy-port must be between 1 and 65535")
	}
	if options.ReconnectInitialDelay <= 0 || options.ReconnectMaxDelay < options.ReconnectInitialDelay {
		return CaptureOptions{}, errors.New("reconnect delays must be positive and maximum must not be less than initial")
	}
	if strings.TrimSpace(options.ExpectedGatewayHost) == "" {
		return CaptureOptions{}, errors.New("--expected-gateway-host is required")
	}
	options.PassthroughHosts, err = normalizePassthroughHosts(passthrough)
	if err != nil {
		return CaptureOptions{}, err
	}
	return options, nil
}

func normalizePassthroughHosts(values []string) ([]string, error) {
	seen := make(map[string]struct{})
	for _, host := range values {
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		if net.ParseIP(host) == nil && !validProxyHost(host) {
			return nil, fmt.Errorf("invalid --passthrough-host %q", host)
		}
		seen[host] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for host := range seen {
		result = append(result, host)
	}
	sort.Strings(result)
	return result, nil
}

func validProxyHost(host string) bool {
	if host == "" || len(host) > 253 || strings.ContainsAny(host, "/\\:@ \t\r\n") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
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
	var passthrough stringFlags
	protocolVersion, err := envInt("MOCKINGO_TUNNEL_PROTOCOL_VERSION", tunnelprotocol.Version)
	if err != nil {
		return ExposeOptions{}, err
	}
	reconnectEnabled, err := envBool("MOCKINGO_RECONNECT_ENABLED", true)
	if err != nil {
		return ExposeOptions{}, err
	}
	dependencyProxy, err := envBool("MOCKINGO_DEPENDENCY_PROXY_ENABLED", true)
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
	options.DependencyProxy = dependencyProxy
	options.AllowInsecureGateway = allowInsecure
	options.AllowFileCredentials = allowFile
	options.ReconnectInitialDelay = initialDelay
	options.ReconnectMaxDelay = maxDelay
	set := flag.NewFlagSet("expose", flag.ContinueOnError)
	set.SetOutput(new(strings.Builder))
	set.StringVar(&options.Name, "name", "", "tunnel name")
	set.IntVar(&options.HTTPPort, "http", 0, "local HTTP port")
	set.BoolVar(&options.DependencyProxy, "dependency-proxy", options.DependencyProxy, "run dependency capture and replay proxy")
	set.IntVar(&options.ProxyPort, "proxy-port", 8899, "loopback dependency proxy port")
	set.Var(&passthrough, "passthrough-host", "exact HTTPS hostname to tunnel without inspection")
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
	if options.ProxyPort < 1 || options.ProxyPort > 65535 {
		return ExposeOptions{}, errors.New("--proxy-port must be between 1 and 65535")
	}
	if options.DependencyProxy && options.ProxyPort == options.HTTPPort {
		return ExposeOptions{}, errors.New("--proxy-port must differ from --http when the dependency proxy is enabled")
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
	options.PassthroughHosts, err = normalizePassthroughHosts(passthrough)
	if err != nil {
		return ExposeOptions{}, err
	}
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
