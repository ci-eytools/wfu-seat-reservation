// Command wfuseat is a terminal client for the WFU seat system.
//
// Queries use a read-only transport guard. Scheduled reservation writes are
// allowed only after an account-scoped job is explicitly created by the user.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"wfuseat/internal/launcher"
	"wfuseat/internal/schedule"
	"wfuseat/internal/servercmd"
	"wfuseat/internal/storage"

	tea "github.com/charmbracelet/bubbletea"

	"wfuseat/internal/config"
	"wfuseat/internal/tui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "错误："+err.Error())
		os.Exit(1)
	}
}

func run() error {
	var (
		sshTarget    = flag.String("ssh", "", "通过 SSH 连接远程后端，user@host")
		sshKey       = flag.String("ssh-key", "", "SSH 私钥路径")
		sshPort      = flag.Int("ssh-port", 22, "SSH 端口")
		serve        = flag.Bool("serve", false, "运行远程后端与调度器")
		server       = flag.String("server", "", "连接远程后端 URL；留空使用本地模式")
		listen       = flag.String("listen", "127.0.0.1:8787", "后端监听地址")
		tlsCert      = flag.String("tls-cert", "", "HTTPS 证书")
		tlsKey       = flag.String("tls-key", "", "HTTPS 私钥")
		account      = flag.String("account", "", "账号名；每个账号独立登录与保存数据")
		listAccounts = flag.Bool("list-accounts", false, "列出账号")
		worker       = flag.Bool("worker", false, "仅运行所有账号的后台预约调度器")
		jobs         = flag.Bool("jobs", false, "显示当前账号的预订任务后退出")
		proxy        = flag.String("proxy", "", "覆盖配置中的代理地址（例如 http://127.0.0.1:7890；留空表示直连）")
		logDir       = flag.String("log-dir", "", "覆盖脱敏请求日志目录")
		configDir    = flag.String("config-dir", "", "覆盖配置目录（默认使用系统用户配置目录）")
		asciiOnly    = flag.Bool("ascii", false, "使用 ASCII 回退字符，适配无 Unicode 支持的终端")
		printCfg     = flag.Bool("print-config", false, "打印生效配置与路径后退出")
	)
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, "wfuseat — WFU 座位终端客户端\n\n用法：%s [选项]\n\n选项：\n", os.Args[0])
		flag.PrintDefaults()
		fmt.Fprintf(out, "\na 创建自动预订后按计划真实执行；--worker 可单独运行所有账号的调度器。\n")
	}
	flag.Parse()

	if *configDir != "" {
		if err := os.Setenv("WFUSEAT_CONFIG_DIR", *configDir); err != nil {
			return err
		}
	}

	root, err := config.Dir()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *sshTarget != "" {
		if *serve || *server != "" || *worker {
			return fmt.Errorf("--ssh 不能与 --serve、--server 或 --worker 混用")
		}
		return launcher.SSH(ctx, root, *sshTarget, *sshKey, *sshPort, *account, *listAccounts, *jobs)
	}
	if (*serve && (*server != "" || *worker || *jobs || *listAccounts)) || (*server != "" && *worker) {
		return fmt.Errorf("--serve、--server 和本地 --worker 模式不能混用")
	}
	if *serve {
		return servercmd.Run(ctx, root, *listen, *tlsCert, *tlsKey, *proxy)
	}
	if *server != "" {
		return launcher.Remote(ctx, root, *server, *account, *listAccounts, *jobs)
	}
	if *listAccounts {
		ids, err := storage.Accounts(root)
		if err != nil {
			return err
		}
		for _, id := range ids {
			fmt.Println(id)
		}
		return nil
	}
	if *account != "" {
		ids, e := storage.Accounts(root)
		if e != nil {
			return e
		}
		exists := false
		for _, id := range ids {
			if id == *account {
				exists = true
			}
		}
		if !exists {
			return fmt.Errorf("账号不存在，请启动界面后按 n 扫码新增")
		}
	}
	if *jobs && *account == "" {
		ids, e := storage.Accounts(root)
		if e != nil {
			return e
		}
		if len(ids) == 0 {
			return fmt.Errorf("尚未登录账号")
		}
		*account = ids[0]
	}
	if *jobs {
		b, err := schedule.ExportJobs(root, *account)
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	if *worker {
		schedule.Serve(ctx, root)
		return nil
	}
	if *account == "" {
		ids, e := storage.Accounts(root)
		if e != nil {
			return e
		}
		for _, id := range ids {
			if !strings.HasPrefix(id, "pending-") {
				*account = id
				break
			}
		}
		if *account == "" {
			*account = fmt.Sprintf("pending-%d", time.Now().UnixNano())
		}
	}
	cfg, path, err := config.LoadAccount(root, *account)
	if err != nil {
		return err
	}

	// Only flags the user actually passed override the stored configuration, so
	// an explicit empty value can still mean "no proxy".
	explicit := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if explicit["proxy"] {
		cfg.Proxy = *proxy
	}
	if explicit["log-dir"] && *logDir != "" {
		return fmt.Errorf("多账号模式的日志固定在各账号目录，不能共用 --log-dir")
	}
	if *asciiOnly {
		cfg.ASCIIOnly = true
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return err
	}

	if *printCfg {
		raw, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		fmt.Printf("配置文件：%s\n%s\n", path, raw)
		return nil
	}

	if _, err = config.Save(cfg); err != nil {
		return err
	}
	model, err := tui.New(cfg, path)
	if err != nil {
		return err
	}
	done := make(chan struct{})
	go func() { defer close(done); schedule.Serve(ctx, root) }()
	defer func() { cancel(); <-done }()
	defer model.Close()

	// Mouse reporting is intentionally left off so terminal text selection keeps
	// working, which matters for reading the prepared form.
	program := tea.NewProgram(model, tea.WithAltScreen())
	final, err := program.Run()
	if last, ok := final.(*tui.Model); ok && last != model {
		last.Close()
	}
	if err != nil {
		return fmt.Errorf("TUI 运行失败: %w", err)
	}
	return nil
}
