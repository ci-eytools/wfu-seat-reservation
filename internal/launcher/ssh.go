package launcher

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"wfuseat/internal/api"
)

// SSH uses the installed OpenSSH client and its known_hosts verification.
func SSH(ctx context.Context, root, target, key string, port int, selected string, list, jobs bool) error {
	if target == "" || strings.HasPrefix(target, "-") || strings.ContainsAny(target, " /") || strings.IndexFunc(target, unicode.IsSpace) >= 0 || port < 1 || port > 65535 {
		return fmt.Errorf("SSH 地址应为 user@host，端口应为 1–65535")
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("请先安装 OpenSSH 客户端: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	addr := listener.Addr().String()
	listener.Close()
	args := []string{"-N", "-T", "-p", strconv.Itoa(port),
		"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "ExitOnForwardFailure=yes", "-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2",
		"-o", "ProxyCommand=none", "-o", "ProxyJump=none",
		"-o", "KexAlgorithms=curve25519-sha256",
		"-L", addr + ":127.0.0.1:8787"}
	if key != "" {
		args = append(args, "-o", "IdentitiesOnly=yes", "-i", key)
	}
	args = append(args, target)
	tunnelCtx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := exec.CommandContext(tunnelCtx, "ssh", args...)
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() { stop(); <-done }()
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
ready:
	for {
		select {
		case err = <-done:
			done <- err
			return fmt.Errorf("SSH 隧道启动失败，请检查密钥和 known_hosts: %v", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("SSH 隧道连接超时")
		case <-tick.C:
			c, e := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if e == nil {
				c.Close()
				break ready
			}
		}
	}
	// Stable identity keeps credentials independent of the ephemeral local port.
	identity := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", target, port)))
	client, err := api.NewClient("http://127.0.0.1:8787", filepath.Join(root, fmt.Sprintf("ssh-%x", identity[:16])))
	if err != nil {
		return err
	}
	transport := client.HTTP.Transport.(*http.Transport)
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", addr)
	}
	defer transport.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(ctx, "GET", client.BaseURL+"/v1/health", nil)
	response, err := client.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("SSH 已连接，但后端不可达: %w", err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("后端健康检查失败: HTTP %d", response.StatusCode)
	}
	return remoteClient(ctx, client, selected, list, jobs)
}
