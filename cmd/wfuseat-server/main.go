package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"wfuseat/internal/config"
	"wfuseat/internal/servercmd"
)

func run() error {
	listen := flag.String("listen", "127.0.0.1:8787", "监听地址与端口")
	cert := flag.String("tls-cert", "", "HTTPS 证书")
	key := flag.String("tls-key", "", "HTTPS 私钥")
	proxy := flag.String("proxy", "", "后端访问学校的代理；默认直连")
	rootFlag := flag.String("config-dir", "", "后端数据根目录")
	flag.Parse()
	if *rootFlag != "" {
		if e := os.Setenv("WFUSEAT_CONFIG_DIR", *rootFlag); e != nil {
			return e
		}
	}
	root, e := config.Dir()
	if e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return servercmd.Run(ctx, root, *listen, *cert, *key, *proxy)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "错误："+err.Error())
		os.Exit(1)
	}
}
