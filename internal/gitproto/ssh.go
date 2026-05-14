package gitproto

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strings"
	"sync"
)

// SSHLookPath is replaceable in tests.
var SSHLookPath = exec.LookPath

// SSHConn represents a Git transport over the local ssh binary.
type SSHConn struct {
	Label       string
	EndpointURL *url.URL
	sshPath     string
	progressOut io.Writer
}

// NewSSHConn creates a new SSH transport connection backed by the local ssh
// binary.
func NewSSHConn(ep *url.URL, label string) (*SSHConn, error) {
	sshPath, err := SSHLookPath("ssh")
	if err != nil {
		return nil, fmt.Errorf("locate ssh binary: %w", err)
	}
	normalizeEndpointPath(ep)
	return &SSHConn{
		Label:       label,
		EndpointURL: ep,
		sshPath:     sshPath,
	}, nil
}

func (c *SSHConn) Endpoint() *url.URL { return c.EndpointURL }

func (c *SSHConn) ProgressWriter() io.Writer { return c.progressOut }

func (c *SSHConn) SetProgressWriter(w io.Writer) { c.progressOut = w }

func (c *SSHConn) Close() error { return nil }

func (c *SSHConn) RequestInfoRefs(ctx context.Context, service string, gitProtocol string) ([]byte, error) {
	cmd, stderr, err := c.startRPC(ctx, service, gitProtocol)
	if err != nil {
		return nil, err
	}
	if err := cmd.Stdin.Close(); err != nil {
		return nil, fmt.Errorf("close ssh stdin for %s: %w", service, err)
	}
	data, readErr := io.ReadAll(cmd.Stdout)
	waitErr := cmd.wait()
	if ctx.Err() != nil {
		return nil, errors.Join(ctx.Err(), readErr, stderr.wrap(waitErr))
	}
	if readErr != nil {
		return nil, fmt.Errorf("%s info-refs: %w", service, readErr)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("%s info-refs: %w", service, stderr.wrap(waitErr))
	}
	return data, nil
}

func (c *SSHConn) PostRPCStreamBody(ctx context.Context, service string, body io.Reader, v2 bool, phase string) (io.ReadCloser, error) {
	_ = phase
	gitProtocol := ""
	if v2 {
		gitProtocol = "version=2"
	}
	cmd, stderr, err := c.startRPC(ctx, service, gitProtocol)
	if err != nil {
		return nil, err
	}
	copyErr := make(chan error, 1)
	go func() {
		_, err := io.Copy(cmd.Stdin, body)
		closeErr := cmd.Stdin.Close()
		if err != nil {
			copyErr <- err
			return
		}
		copyErr <- closeErr
	}()
	return &sshRPCStream{
		ctx:     ctx,
		stdout:  cmd.Stdout,
		wait:    cmd.wait,
		copyErr: copyErr,
		stderr:  stderr,
	}, nil
}

func (c *SSHConn) startRPC(ctx context.Context, service string, gitProtocol string) (*sshCommand, *sshCommandError, error) {
	args, err := sshInvocationArgs(c.EndpointURL, service, gitProtocol)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.CommandContext(ctx, c.sshPath, args...)
	stderr := &sshCommandError{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("open ssh stdout for %s: %w", service, err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("open ssh stdin for %s: %w", service, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start ssh for %s: %w", service, stderr.wrap(err))
	}
	return &sshCommand{Cmd: cmd, Stdin: stdin, Stdout: stdout}, stderr, nil
}

func sshInvocationArgs(ep *url.URL, service string, gitProtocol string) ([]string, error) {
	destination, err := sshDestination(ep)
	if err != nil {
		return nil, err
	}
	remoteCommand, err := sshRemoteCommand(ep, service, gitProtocol)
	if err != nil {
		return nil, err
	}
	args := []string{"-o", "BatchMode=yes"}
	if port := ep.Port(); port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, destination, remoteCommand)
	return args, nil
}

func sshDestination(ep *url.URL) (string, error) {
	if ep == nil || ep.Hostname() == "" {
		return "", fmt.Errorf("missing SSH host")
	}
	host := ep.Hostname()
	if ep.User != nil && ep.User.Username() != "" {
		return ep.User.Username() + "@" + host, nil
	}
	return host, nil
}

func sshRemoteCommand(ep *url.URL, service string, gitProtocol string) (string, error) {
	if ep == nil || ep.Path == "" {
		return "", fmt.Errorf("missing SSH repository path")
	}
	path := shellQuote(ep.Path)
	if gitProtocol != "" {
		return gitProtocolEnv(gitProtocol) + " " + service + " " + path, nil
	}
	return service + " " + path, nil
}

func gitProtocolEnv(gitProtocol string) string {
	return "GIT_PROTOCOL=" + shellQuote(gitProtocol)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

type sshCommand struct {
	Cmd    *exec.Cmd
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
}

func (c *sshCommand) wait() error { return c.Cmd.Wait() }

type sshRPCStream struct {
	ctx     context.Context
	stdout  io.ReadCloser
	wait    func() error
	copyErr <-chan error
	stderr  *sshCommandError

	waitOnce sync.Once
	waitErr  error
}

func (s *sshRPCStream) Read(p []byte) (int, error) {
	return s.stdout.Read(p)
}

func (s *sshRPCStream) Close() error {
	closeErr := s.stdout.Close()
	s.waitOnce.Do(func() {
		copyErr := <-s.copyErr
		waitErr := s.wait()
		if copyErr != nil {
			s.waitErr = copyErr
			if waitErr != nil {
				s.waitErr = errors.Join(copyErr, s.stderr.wrap(waitErr))
			}
			return
		}
		if waitErr != nil {
			s.waitErr = s.stderr.wrap(waitErr)
		}
		if s.ctx != nil && s.ctx.Err() != nil {
			s.waitErr = errors.Join(s.ctx.Err(), s.waitErr)
		}
	})
	return errors.Join(closeErr, s.waitErr)
}

type sshCommandError struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (e *sshCommandError) Write(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.buf.Write(p)
}

func (e *sshCommandError) String() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.TrimSpace(e.buf.String())
}

func (e *sshCommandError) wrap(err error) error {
	if err == nil {
		return nil
	}
	if msg := e.String(); msg != "" {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return err
}
