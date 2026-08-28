package tokensync

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

func TestSSHOptionsValidate(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		options   SSHOptions
		expectErr bool
	}{
		{
			name: "ssh disabled",
		},
		{
			name: "valid ssh config",
			options: SSHOptions{
				Host:           "ssh.example.test",
				Port:           1457,
				User:           "test-user",
				PrivateKeyPath: "/tmp/test-key",
			},
		},
		{
			name: "missing host",
			options: SSHOptions{
				User:           "test-user",
				PrivateKeyPath: "/tmp/test-key",
			},
			expectErr: true,
		},
		{
			name: "missing user",
			options: SSHOptions{
				Host:           "ssh.example.test",
				PrivateKeyPath: "/tmp/test-key",
			},
			expectErr: true,
		},
		{
			name: "missing private key",
			options: SSHOptions{
				Host: "ssh.example.test",
				User: "test-user",
			},
			expectErr: true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.options.Validate()
			if tc.expectErr && err == nil {
				t.Fatal("Validate() error = nil, want error")
			}
			if !tc.expectErr && err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
		})
	}
}

func TestExpandSSHPath(t *testing.T) {
	t.Parallel()

	got, err := expandSSHPath("~/test-key")
	if err != nil {
		t.Fatalf("expandSSHPath() error = %v", err)
	}
	if got == "~/test-key" {
		t.Fatal("expandSSHPath() did not expand home path")
	}
}

func TestResolveSSHTrustStorePath(t *testing.T) {
	t.Parallel()

	got, err := resolveSSHTrustStorePath("")
	if err != nil {
		t.Fatalf("resolveSSHTrustStorePath() error = %v", err)
	}
	if got == "" {
		t.Fatal("resolveSSHTrustStorePath() returned empty path")
	}
}

func TestDefaultSSHTrustStorePath(t *testing.T) {
	t.Parallel()

	if got := DefaultSSHTrustStorePath(); got == "" {
		t.Fatal("DefaultSSHTrustStorePath() returned empty path")
	}
}

func TestDialContextOverSSHClosesConnOnContextCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	dialStarted := make(chan struct{})
	releaseDial := make(chan struct{})
	conn := &testConn{}

	resultCh := make(chan error, 1)
	go func() {
		_, err := dialContextOverSSH(ctx, func() (net.Conn, error) {
			close(dialStarted)
			<-releaseDial
			return conn, nil
		})
		resultCh <- err
	}()

	<-dialStarted
	cancel()

	select {
	case err := <-resultCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("dialContextOverSSH() error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("dialContextOverSSH() did not return after context cancellation")
	}

	close(releaseDial)

	deadline := time.After(time.Second)
	for {
		if conn.closed() {
			return
		}

		select {
		case <-deadline:
			t.Fatal("dialContextOverSSH() did not close late connection result")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type testConn struct {
	mu       sync.Mutex
	isClosed bool
}

func (c *testConn) Read(_ []byte) (int, error)  { return 0, net.ErrClosed }
func (c *testConn) Write(_ []byte) (int, error) { return 0, net.ErrClosed }

func (c *testConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.isClosed = true
	return nil
}

func (c *testConn) LocalAddr() net.Addr                { return testAddr("local") }
func (c *testConn) RemoteAddr() net.Addr               { return testAddr("remote") }
func (c *testConn) SetDeadline(_ time.Time) error      { return nil }
func (c *testConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *testConn) SetWriteDeadline(_ time.Time) error { return nil }

func (c *testConn) closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isClosed
}

type testAddr string

func (a testAddr) Network() string { return "tcp" }
func (a testAddr) String() string  { return string(a) }
