package cli

import (
	"net"
	"strings"
	"testing"
	"time"
)

func serveTestAddr() string {
	return "127.0.0.1:18421"
}

func TestServeMissingToken(t *testing.T) {
	env, _, stderr := testEnv(t)
	addr := serveTestAddr()
	code := runArgs(env, "serve", "--addr", addr)
	if code != exitForbidden {
		t.Fatalf("serve without token = %d, want %d, stderr=%s", code, exitForbidden, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "local API token is not set") {
		t.Fatalf("stderr misses message: %q", got)
	}
	if got := stderr.String(); !strings.Contains(got, "VENDING_SECRET_CONN_LOCAL_APITOKEN") {
		t.Fatalf("stderr misses env name: %q", got)
	}
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatal("serve started a listener despite the missing token")
	}
}

func TestServeInvalidAddr(t *testing.T) {
	env, _, stderr := testEnv(t)
	for _, addr := range []string{"localhost:8420", "0.0.0.0:8420", "[::1]:8420", "127.0.0.1:0"} {
		stderr.Reset()
		code := runArgs(env, "serve", "--addr", addr)
		if code != exitUsage {
			t.Fatalf("serve --addr %q = %d, want %d, stderr=%s", addr, code, exitUsage, stderr.String())
		}
	}
}
