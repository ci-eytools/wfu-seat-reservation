package servercmd

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"wfuseat/internal/apiserver"
)

func Run(ctx context.Context, root, listen, cert, key, proxy string) error {
	if (cert == "") != (key == "") {
		return fmt.Errorf("证书与私钥必须同时提供")
	}
	host, _, e := net.SplitHostPort(listen)
	if e != nil {
		return e
	}
	ip := net.ParseIP(host)
	if cert == "" && host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("公网监听需要 TLS；使用反向代理时请监听 127.0.0.1")
	}
	data := filepath.Join(root, "server")
	srv, e := apiserver.New(apiserver.Options{Root: data, Proxy: proxy})
	if e != nil {
		return e
	}
	defer srv.Close()
	fmt.Printf("后端监听 %s；数据目录 %s\n", listen, data)
	return srv.Run(ctx, listen, cert, key)
}
