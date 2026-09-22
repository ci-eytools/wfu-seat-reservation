package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"wfuseat/internal/config"
	"wfuseat/internal/launcher"
)

func run() error {
	server := flag.String("server", "", "指定并记住后端服务 URL；未指定时使用上次地址")
	choose := flag.Bool("choose-server", false, "启动时打开后端地址配置界面")
	account := flag.String("account", "", "选择已扫码的账号")
	list := flag.Bool("list-accounts", false, "列出此后端已保存账号")
	jobs := flag.Bool("jobs", false, "显示本人任务")
	rootFlag := flag.String("config-dir", "", "客户端配置目录")
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
	endpoint, e := launcher.ResolveServer(ctx, root, *server, *choose, *list || *jobs)
	if e != nil {
		return e
	}
	if endpoint == "" {
		return nil
	}
	return launcher.Remote(ctx, root, endpoint, *account, *list, *jobs)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "错误："+err.Error())
		os.Exit(1)
	}
}
