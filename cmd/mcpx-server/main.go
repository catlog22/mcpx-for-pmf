package main

import (
	"flag"
	"fmt"
	"os"

	"mcpx/internal/logging"
	"mcpx/internal/server"
	buildversion "mcpx/internal/version"
)

// Set by GoReleaser / -ldflags at release build time.
var (
	version = buildversion.Current
	commit  = "none"
	date    = "unknown"
)

func main() {
	// Subcommands (before flag.Parse so they own their flags).
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "oauth-register":
			os.Exit(runOAuthRegister(os.Args[2:]))
		case "help", "-h", "--help":
			printUsage()
			os.Exit(0)
		}
	}

	workspace := flag.String("workspace", "", "register workspace path into global config and use it")
	addr := flag.String("addr", "", "override listen addr host:port")
	logLevel := flag.String("log-level", "", "debug|info|warn|error (or MCPX_LOG_LEVEL)")
	logFormat := flag.String("log-format", "", "text|json (or MCPX_LOG_FORMAT)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = printUsage
	flag.Parse()

	if *showVersion {
		fmt.Printf("mcpx-server %s (commit=%s date=%s)\n", version, commit, date)
		os.Exit(0)
	}

	logging.Init(logging.Options{Level: *logLevel, Format: *logFormat})
	logging.Info("mcpx-server", "version", version, "commit", commit)

	rt, err := server.New(server.Options{
		WorkspaceFlag: *workspace,
		AddrOverride:  *addr,
		Version:       version,
		Commit:        commit,
		Date:          date,
	})
	if err != nil {
		logging.Error("startup failed", "err", err)
		os.Exit(1)
	}
	if err := rt.Start(); err != nil {
		logging.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `mcpx-server — MCPX Runtime

Usage:
  mcpx-server [flags]                 启动 Streamable HTTP 服务
  mcpx-server oauth-register [url]    动态注册 OAuth 客户端（粘贴 ChatGPT 回调 URL）
  mcpx-server -version

oauth-register:
  mcpx-server oauth-register 'https://chatgpt.com/connector/oauth/…'
  mcpx-server oauth-register          # 交互粘贴回调
  mcpx-server oauth-register -base https://mcp.example.com 'https://…'

Flags (server):
`)
	flag.PrintDefaults()
}
