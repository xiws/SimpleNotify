// Package cli implements the notify command line interface.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xiws/SimpleNotify/internal/app"
	"github.com/xiws/SimpleNotify/internal/config"
	"github.com/xiws/SimpleNotify/internal/mail"
)

// version is injected at build time with -ldflags.
var version = "dev"

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	if err := newRoot().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		return 1
	}
	return 0
}

func newRoot() *cobra.Command {
	var configPath string
	root := &cobra.Command{
		Use:           "notify",
		Short:         "SimpleNotify：收到匹配邮件时执行命令的通知服务",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&configPath, "config", "c", "conf.json", "配置文件路径")
	root.AddCommand(
		newServeCommand(&configPath),
		newValidateCommand(&configPath),
		newVersionCommand(),
		newSendCommand(),
	)
	return root
}

func newServeCommand(configPath *string) *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "启动 IMAP 轮询和管理页面",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return app.Run(cmd.Context(), version, *configPath, addr)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "覆盖监听地址，例如 127.0.0.1:8899")
	return cmd
}

func newValidateCommand(configPath *string) *cobra.Command {
	var checkMail bool
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "校验配置文件，可选检查邮箱连接",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			printSummary(cmd.OutOrStdout(), cfg)
			if _, err := exec.LookPath(cfg.Execute.Shell); err != nil {
				return fmt.Errorf("execute.shell %q 不存在: %w", cfg.Execute.Shell, err)
			}

			if checkMail {
				if !cfg.Mail.IsEnabled() {
					return errors.New("--check-mail 需要 mail.enabled=true")
				}
				info, err := mail.Check(context.Background(), cfg.Mail, version)
				if err != nil {
					return fmt.Errorf("邮箱连接失败: %w", err)
				}
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "\n连接成功: %s\n", info.Server)
				fmt.Fprintf(out, "服务器能力: %s\n", strings.Join(info.Capabilities, " "))
				fmt.Fprintf(out, "邮箱 %s: UIDVALIDITY=%d UIDNEXT=%d 邮件数=%d\n",
					info.Mailbox, info.UIDValidity, info.UIDNext, info.Messages)
			}

			fmt.Fprintln(cmd.OutOrStdout(), "\n配置有效")
			return nil
		},
	}
	cmd.Flags().BoolVar(&checkMail, "check-mail", false, "真实连接 IMAP 服务器并登录")
	return cmd
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "打印版本信息",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "notify %s (%s %s/%s)\n",
				version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		},
	}
}

func newSendCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "send",
		Short: "主动发送通知（尚未实现）",
		RunE: func(*cobra.Command, []string) error {
			return errors.New("notify send 尚未实现")
		},
	}
}

func printSummary(out io.Writer, cfg *config.Config) {
	fmt.Fprintf(out, "配置文件: %s\n", cfg.Path)
	fmt.Fprintf(out, "监听地址: %s\n", cfg.Server.Addr)
	if cfg.Server.Password != "" {
		fmt.Fprintln(out, "管理密码: 已设置")
	} else {
		fmt.Fprintln(out, "管理密码: 未设置（仅建议本机访问）")
	}

	if cfg.Mail.IsEnabled() {
		fmt.Fprintf(out, "邮箱: %s (%s, 文件夹 %s, 每 %s 轮询)\n",
			cfg.Mail.Addr(), cfg.Mail.Security, cfg.Mail.Mailbox, cfg.Mail.PollInterval)
		fmt.Fprintf(out, "账号: %s\n", cfg.Mail.Username)
		fmt.Fprintf(out, "首次运行: on_start=%s\n", cfg.Mail.OnStart)
	} else {
		fmt.Fprintln(out, "邮箱: 未启用")
	}

	fmt.Fprintf(out, "执行: %s (超时 %s, 工作目录 %s)\n",
		cfg.Execute.Shell, cfg.Execute.Timeout, cfg.Execute.WorkDir)
	fmt.Fprintf(out, "状态文件: %s\n", cfg.State.File)
	if cfg.History.File != "" {
		fmt.Fprintf(out, "历史文件: %s\n", cfg.History.File)
	} else {
		fmt.Fprintln(out, "历史文件: 未启用（仅内存）")
	}

	enabled := 0
	for _, r := range cfg.Rules {
		if r.IsEnabled() {
			enabled++
		}
	}
	fmt.Fprintf(out, "规则: %d 条（启用 %d）\n", len(cfg.Rules), enabled)
}
