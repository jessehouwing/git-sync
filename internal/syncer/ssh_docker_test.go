package syncer

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"entire.io/entire/git-sync/internal/gitproto"
	"github.com/go-git/go-git/v6/plumbing"
)

const sshDockerEnv = "GITSYNC_E2E_SSH_DOCKER"

func TestRun_SSHDockerSync(t *testing.T) {
	if os.Getenv(sshDockerEnv) == "" {
		t.Skip("set GITSYNC_E2E_SSH_DOCKER=1 to run docker-based SSH integration test")
	}

	for _, tool := range []string{"docker", "git", "ssh", "ssh-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available: %v", tool, err)
		}
	}

	root := dockerBindMountTempDir(t)
	reposDir := filepath.Join(root, "repos")
	worktree := filepath.Join(root, "worktree")
	sourceBare := filepath.Join(reposDir, "source.git")
	targetBare := filepath.Join(reposDir, "target.git")

	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		t.Fatalf("mkdir repos: %v", err)
	}

	runGit(t, root, "init", "--bare", sourceBare)
	runGit(t, root, "init", "--bare", targetBare)
	runGit(t, root, "init", "-b", testBranch, worktree)
	runGit(t, worktree, "config", "user.name", "git-sync test")
	runGit(t, worktree, "config", "user.email", "git-sync@example.com")

	writeFile(t, filepath.Join(worktree, "README.md"), "hello over docker ssh\n")
	runGit(t, worktree, "add", "README.md")
	runGit(t, worktree, "commit", "-m", "initial")
	runGit(t, worktree, "remote", "add", "origin", sourceBare)
	runGit(t, worktree, "push", "origin", "HEAD:refs/heads/"+testBranch)

	keyPath := filepath.Join(root, "id_ed25519")
	runCommand(t, root, "ssh-keygen", "-t", "ed25519", "-N", "", "-f", keyPath)
	pubKey, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatalf("read public key: %v", err)
	}
	pubKeyB64 := base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(string(pubKey))))

	imageCtx := filepath.Join(root, "image")
	writeSSHServerDockerContext(t, imageCtx)

	imageTag := fmt.Sprintf("git-sync-ssh-e2e:%d", rand.New(rand.NewSource(time.Now().UnixNano())).Int63())
	runCommand(t, imageCtx, "docker", "build", "-t", imageTag, ".")
	t.Cleanup(func() {
		if err := runCommandBestEffort(t.Context(), root, "docker", "image", "rm", "-f", imageTag); err != nil {
			t.Logf("cleanup docker image: %v", err)
		}
	})

	containerID := strings.TrimSpace(runCommand(t, root,
		"docker", "run", "-d",
		"-e", "AUTHORIZED_KEY_B64="+pubKeyB64,
		"-P",
		imageTag,
	))
	t.Cleanup(func() {
		if err := runCommandBestEffort(t.Context(), root, "docker", "rm", "-f", containerID); err != nil {
			t.Logf("cleanup docker container: %v", err)
		}
	})

	runCommand(t, root, "docker", "cp", sourceBare, containerID+":/srv/git/source.git")
	runCommand(t, root, "docker", "cp", targetBare, containerID+":/srv/git/target.git")
	runCommand(t, root, "docker", "exec", containerID, "chown", "-R", "git:git", "/srv/git")

	portOutput := strings.TrimSpace(runCommandWithDiagnostics(t, root, containerID,
		"docker", "port", containerID, "22/tcp",
	))
	port, err := parseDockerPort(portOutput)
	if err != nil {
		t.Fatalf("parse docker port %q: %v", portOutput, err)
	}
	if port == "" {
		t.Fatal("docker inspect returned empty SSH host port")
	}

	sshConfigPath := filepath.Join(root, "ssh_config")
	writeFile(t, sshConfigPath, strings.Join([]string{
		"Host gitsync-ssh-docker",
		"  HostName 127.0.0.1",
		"  Port " + port,
		"  User git",
		"  IdentityFile " + keyPath,
		"  IdentitiesOnly yes",
		"  StrictHostKeyChecking no",
		"  UserKnownHostsFile /dev/null",
		"  BatchMode yes",
		"",
	}, "\n"))

	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatalf("locate ssh: %v", err)
	}
	sshWrapper := filepath.Join(root, "ssh-wrapper.sh")
	writeFile(t, sshWrapper, strings.Join([]string{
		"#!/bin/sh",
		"exec " + shSingleQuote(sshPath) + " -F " + shSingleQuote(sshConfigPath) + ` "$@"`,
		"",
	}, "\n"))
	if err := os.Chmod(sshWrapper, 0o755); err != nil {
		t.Fatalf("chmod ssh wrapper: %v", err)
	}

	orig := gitproto.SSHLookPath
	t.Cleanup(func() { gitproto.SSHLookPath = orig })
	gitproto.SSHLookPath = func(string) (string, error) { return sshWrapper, nil }

	waitForSSHReady(t, sshWrapper, containerID)

	result, err := Run(context.Background(), Config{
		Source:       Endpoint{URL: "ssh://gitsync-ssh-docker/srv/git/source.git"},
		Target:       Endpoint{URL: "ssh://gitsync-ssh-docker/srv/git/target.git"},
		ProtocolMode: protocolModeAuto,
	})
	if err != nil {
		t.Fatalf("Run over Docker SSH failed: %v", err)
	}
	if result.Protocol != protocolModeV2 {
		t.Fatalf("result.Protocol = %q, want %q", result.Protocol, protocolModeV2)
	}
	if !result.Relay || result.RelayMode != relayModeBootstrap {
		t.Fatalf("expected bootstrap relay over SSH, got %+v", result)
	}

	syncedTargetBare := filepath.Join(root, "synced-target.git")
	runCommand(t, root, "docker", "cp", containerID+":/srv/git/target.git", syncedTargetBare)
	assertGitRefEqual(t, sourceBare, syncedTargetBare, plumbing.NewBranchReferenceName(testBranch))
}

func writeSSHServerDockerContext(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir docker context: %v", err)
	}

	writeFile(t, filepath.Join(dir, "Dockerfile"), strings.Join([]string{
		"FROM alpine:3.21",
		"RUN apk add --no-cache git openssh-server",
		"RUN adduser -D -h /home/git -s /bin/sh git",
		`RUN sed -i 's/^git:!:/git::/' /etc/shadow`,
		"EXPOSE 22",
		"COPY sshd_config /etc/ssh/sshd_config",
		"COPY entrypoint.sh /entrypoint.sh",
		"RUN chmod +x /entrypoint.sh",
		`ENTRYPOINT ["/entrypoint.sh"]`,
		"",
	}, "\n"))

	writeFile(t, filepath.Join(dir, "sshd_config"), strings.Join([]string{
		"Port 22",
		"ListenAddress 0.0.0.0",
		"PasswordAuthentication no",
		"KbdInteractiveAuthentication no",
		"ChallengeResponseAuthentication no",
		"UsePAM no",
		"PermitRootLogin no",
		"PubkeyAuthentication yes",
		"AuthorizedKeysFile .ssh/authorized_keys",
		"AllowUsers git",
		"LogLevel VERBOSE",
		"Subsystem sftp internal-sftp",
		"PidFile /run/sshd.pid",
		"",
	}, "\n"))

	writeFile(t, filepath.Join(dir, "entrypoint.sh"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		"mkdir -p /run/sshd /home/git/.ssh /srv/git",
		`printf '%s' "$AUTHORIZED_KEY_B64" | base64 -d > /home/git/.ssh/authorized_keys`,
		"chmod 700 /home/git/.ssh",
		"chmod 600 /home/git/.ssh/authorized_keys",
		"chown -R git:git /home/git /srv/git",
		"ssh-keygen -A >/dev/null 2>&1",
		"exec /usr/sbin/sshd -D -e",
		"",
	}, "\n"))
}

func dockerBindMountTempDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func waitForSSHReady(t *testing.T, sshPath string, containerID string) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		cmd := exec.CommandContext(t.Context(), sshPath, "gitsync-ssh-docker", "true")
		output, err := cmd.CombinedOutput()
		if err == nil {
			return
		}
		lastErr = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
		time.Sleep(200 * time.Millisecond)
	}
	state := strings.TrimSpace(dockerCommandBestEffortOutput(t.Context(), "", "ps", "-a", "--filter", "id="+containerID, "--format", "{{.Status}}"))
	logs := strings.TrimSpace(dockerCommandBestEffortOutput(t.Context(), "", "logs", containerID))
	t.Fatalf("ssh server did not become ready: %v\ncontainer-status: %s\ncontainer-logs:\n%s", lastErr, state, logs)
}

func runCommand(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, output)
	}
	return string(output)
}

func runCommandWithDiagnostics(t *testing.T, dir string, containerID string, name string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		state := strings.TrimSpace(dockerCommandBestEffortOutput(t.Context(), dir, "ps", "-a", "--filter", "id="+containerID, "--format", "{{.Status}}"))
		logs := strings.TrimSpace(dockerCommandBestEffortOutput(t.Context(), dir, "logs", containerID))
		t.Fatalf("%s %s failed: %v\n%s\ncontainer-status: %s\ncontainer-logs:\n%s", name, strings.Join(args, " "), err, output, state, logs)
	}
	return string(output)
}

func runCommandBestEffort(ctx context.Context, dir string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s failed: %w\n%s", name, strings.Join(args, " "), err, output)
	}
	return nil
}

func dockerCommandBestEffortOutput(ctx context.Context, dir string, args ...string) string {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output)
	}
	return string(output)
}

func parseDockerPort(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("empty port mapping")
	}

	if newline := strings.IndexByte(s, '\n'); newline >= 0 {
		s = strings.TrimSpace(s[:newline])
	}

	idx := strings.LastIndexByte(s, ':')
	if idx < 0 {
		return "", errors.New("missing host port separator")
	}
	port := strings.TrimSpace(s[idx+1:])
	if port == "" {
		return "", errors.New("empty host port")
	}
	return port, nil
}
