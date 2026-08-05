package process

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Result struct {
	Code int
	Err  error
}

type Process struct {
	cmd      *exec.Cmd
	done     chan Result
	stopOnce sync.Once
}

type Options struct {
	Command []string
	CWD     string
	Env     map[string]string
	Stdout  io.Writer
	Stderr  io.Writer
}

func Start(options Options) (*Process, error) {
	if len(options.Command) == 0 || strings.TrimSpace(options.Command[0]) == "" {
		return nil, fmt.Errorf("process command is required")
	}
	name := options.Command[0]
	args := append([]string(nil), options.Command[1:]...)
	if runtime.GOOS == "windows" {
		ext := strings.ToLower(filepath.Ext(name))
		if ext == ".bat" || ext == ".cmd" {
			args = append([]string{"/C", name}, args...)
			name = "cmd.exe"
		}
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = options.CWD
	cmd.Stdout = options.Stdout
	cmd.Stderr = options.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = mergeEnvironment(os.Environ(), options.Env)
	configureProcess(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start process: %w", err)
	}
	proc := &Process{cmd: cmd, done: make(chan Result, 1)}
	go func() {
		err := cmd.Wait()
		result := Result{Err: err}
		if cmd.ProcessState != nil {
			result.Code = cmd.ProcessState.ExitCode()
		}
		proc.done <- result
		close(proc.done)
	}()
	return proc, nil
}

func mergeEnvironment(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	result := append([]string(nil), base...)
	for key, value := range overrides {
		prefix := key + "="
		replaced := false
		for i, item := range result {
			match := strings.HasPrefix(item, prefix)
			if runtime.GOOS == "windows" {
				match = strings.EqualFold(strings.SplitN(item, "=", 2)[0], key)
			}
			if match {
				result[i] = prefix + value
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, prefix+value)
		}
	}
	return result
}

func (p *Process) Done() <-chan Result { return p.done }

func (p *Process) Stop(ctx context.Context) error {
	var stopErr error
	p.stopOnce.Do(func() {
		stopErr = gracefulStop(p.cmd)
		select {
		case <-p.done:
			return
		case <-ctx.Done():
			_ = forceStop(p.cmd)
			select {
			case <-p.done:
			case <-time.After(2 * time.Second):
			}
		}
	})
	return stopErr
}
