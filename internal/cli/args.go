package cli

import (
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type envFlags []string

func (f *envFlags) String() string { return strings.Join(*f, ",") }
func (f *envFlags) Set(value string) error {
	*f = append(*f, value)
	return nil
}

type ExposeOptions struct {
	Name           string
	HTTPPort       int
	CWD            string
	Environment    map[string]string
	StartupTimeout time.Duration
	RequestTimeout time.Duration
	Verbose        bool
	Command        []string
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
	set := flag.NewFlagSet("expose", flag.ContinueOnError)
	set.SetOutput(new(strings.Builder))
	set.StringVar(&options.Name, "name", "", "tunnel name")
	set.IntVar(&options.HTTPPort, "http", 0, "local HTTP port")
	set.StringVar(&options.CWD, "cwd", "", "child working directory")
	set.Var(&env, "env", "child environment KEY=VALUE")
	set.DurationVar(&options.StartupTimeout, "startup-timeout", 60*time.Second, "startup timeout")
	set.DurationVar(&options.RequestTimeout, "request-timeout", 60*time.Second, "request timeout")
	set.BoolVar(&options.Verbose, "verbose", false, "verbose diagnostics")
	if err := set.Parse(args); err != nil {
		return ExposeOptions{}, err
	}
	if options.Name == "" {
		return ExposeOptions{}, errors.New("--name is required")
	}
	if options.HTTPPort < 1 || options.HTTPPort > 65535 {
		return ExposeOptions{}, errors.New("--http must be a port between 1 and 65535")
	}
	if options.StartupTimeout <= 0 || options.RequestTimeout <= 0 {
		return ExposeOptions{}, errors.New("timeouts must be greater than zero")
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
