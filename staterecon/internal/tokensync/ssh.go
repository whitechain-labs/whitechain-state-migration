package tokensync

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type sshTunnel struct {
	client *ssh.Client
}

func newSSHTunnel(ctx context.Context, opts SSHOptions) (*sshTunnel, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	keyPath, err := expandSSHPath(opts.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("resolve ssh private key path: %w", err)
	}
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read ssh private key %q: %w", keyPath, err)
	}

	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("parse ssh private key %q: %w", keyPath, err)
	}

	hostKeyCallback, err := buildHostKeyCallback(opts)
	if err != nil {
		return nil, err
	}

	sshAddress := net.JoinHostPort(strings.TrimSpace(opts.Host), strconv.Itoa(opts.PortOrDefault()))
	dialer := &net.Dialer{}
	netConn, err := dialer.DialContext(ctx, "tcp", sshAddress)
	if err != nil {
		return nil, fmt.Errorf("dial ssh host %s: %w", sshAddress, err)
	}

	clientConn, channels, requests, err := ssh.NewClientConn(netConn, sshAddress, &ssh.ClientConfig{
		User:            strings.TrimSpace(opts.User),
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKeyCallback,
	})
	if err != nil {
		_ = netConn.Close()
		return nil, fmt.Errorf("establish ssh connection to %s: %w", sshAddress, err)
	}

	log.WithFields(log.Fields{
		"ssh_host": sshAddress,
		"ssh_user": strings.TrimSpace(opts.User),
	}).Info("using SSH tunnel for Blockscout Postgres connection")

	return &sshTunnel{
		client: ssh.NewClient(clientConn, channels, requests),
	}, nil
}

func (t *sshTunnel) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if t == nil || t.client == nil {
		return nil, fmt.Errorf("ssh tunnel is not initialized")
	}

	conn, err := dialContextOverSSH(ctx, func() (net.Conn, error) {
		return t.client.Dial(network, addr)
	})
	if err != nil {
		return nil, fmt.Errorf("dial %s over ssh: %w", addr, err)
	}

	return conn, nil
}

func dialContextOverSSH(ctx context.Context, dial func() (net.Conn, error)) (net.Conn, error) {
	type dialResult struct {
		conn net.Conn
		err  error
	}

	resultCh := make(chan dialResult, 1)
	go func() {
		conn, err := dial()
		resultCh <- dialResult{conn: conn, err: err}
	}()

	select {
	case <-ctx.Done():
		go func() {
			result := <-resultCh
			if result.conn != nil {
				_ = result.conn.Close()
			}
		}()
		return nil, ctx.Err()
	case result := <-resultCh:
		return result.conn, result.err
	}
}

func (t *sshTunnel) Close() error {
	if t == nil || t.client == nil {
		return nil
	}
	return t.client.Close()
}

func (o SSHOptions) Validate() error {
	if !o.Enabled() {
		return nil
	}

	switch {
	case strings.TrimSpace(o.Host) == "":
		return fmt.Errorf("ssh host is required when ssh tunnel is enabled")
	case strings.TrimSpace(o.User) == "":
		return fmt.Errorf("ssh user is required when ssh tunnel is enabled")
	case strings.TrimSpace(o.PrivateKeyPath) == "":
		return fmt.Errorf("ssh private key file is required when ssh tunnel is enabled")
	case o.PortOrDefault() <= 0 || o.PortOrDefault() > 65535:
		return fmt.Errorf("ssh port %d is invalid", o.Port)
	default:
		return nil
	}
}

func buildHostKeyCallback(opts SSHOptions) (ssh.HostKeyCallback, error) {
	trustStorePath, err := resolveSSHTrustStorePath(opts.TrustStorePath)
	if err != nil {
		return nil, fmt.Errorf("resolve ssh trust store path: %w", err)
	}

	if err := ensureSSHTrustStore(trustStorePath); err != nil {
		return nil, fmt.Errorf("prepare ssh trust store %q: %w", trustStorePath, err)
	}

	knownHostsCallback, err := knownhosts.New(trustStorePath)
	if err != nil {
		return nil, fmt.Errorf("load ssh trust store %q: %w", trustStorePath, err)
	}

	var mu sync.Mutex
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		mu.Lock()
		defer mu.Unlock()

		if err := knownHostsCallback(hostname, remote, key); err == nil {
			return nil
		} else {
			var keyErr *knownhosts.KeyError
			if !errors.As(err, &keyErr) {
				return err
			}
			if len(keyErr.Want) != 0 {
				return fmt.Errorf("ssh host key mismatch for %s: %w", hostname, err)
			}

			if err := appendSSHTrustedHost(trustStorePath, hostname, key); err != nil {
				return fmt.Errorf("trust-on-first-use save failed for %s: %w", hostname, err)
			}

			reloadedCallback, reloadErr := knownhosts.New(trustStorePath)
			if reloadErr != nil {
				return fmt.Errorf("reload ssh trust store %q: %w", trustStorePath, reloadErr)
			}
			knownHostsCallback = reloadedCallback

			log.WithFields(log.Fields{
				"ssh_host":        hostname,
				"ssh_fingerprint": ssh.FingerprintSHA256(key),
				"trust_store":     trustStorePath,
			}).Info("trusted ssh host key on first use")
			return nil
		}
	}, nil
}

func resolveSSHTrustStorePath(path string) (string, error) {
	if strings.TrimSpace(path) != "" {
		return expandSSHPath(path)
	}

	return defaultSSHTrustStorePath()
}

func defaultSSHTrustStorePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("detect user config directory: %w", err)
	}

	return filepath.Join(configDir, "whitechain-utils", "tokensync_known_hosts"), nil
}

// DefaultSSHTrustStorePath returns the default TOFU trust store path used by tokensync.
func DefaultSSHTrustStorePath() string {
	path, err := defaultSSHTrustStorePath()
	if err != nil {
		return ""
	}
	return path
}

func ensureSSHTrustStore(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	file, err := os.OpenFile(path, os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	return file.Close()
}

func appendSSHTrustedHost(path, hostname string, key ssh.PublicKey) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()

	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
	if _, err := file.WriteString(line + "\n"); err != nil {
		return err
	}
	return nil
}

func expandSSHPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("path is empty")
	}

	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("detect home directory: %w", err)
		}

		if trimmed == "~" {
			return homeDir, nil
		}
		return filepath.Join(homeDir, strings.TrimPrefix(trimmed, "~/")), nil
	}

	return trimmed, nil
}
