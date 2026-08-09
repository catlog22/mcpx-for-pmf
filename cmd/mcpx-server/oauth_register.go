package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"mcpx/internal/config"
)

// runOAuthRegister implements:
//
//	mcpx oauth-register [callback-url]
//	mcpx oauth-register -base https://mcp.example.com [callback-url]
//
// Registers a public OAuth client (RFC7591 DCR) and prints client_id for ChatGPT.
func runOAuthRegister(args []string) int {
	fs := flag.NewFlagSet("oauth-register", flag.ExitOnError)
	base := fs.String("base", "", "MCPX public origin (default: auth.oauth.server_url or http://127.0.0.1:<port>)")
	name := fs.String("name", "ChatGPT", "client_name sent to registration")
	_ = fs.Parse(args)

	cb := strings.TrimSpace(fs.Arg(0))
	if cb == "" {
		fmt.Fprint(os.Stderr, "粘贴 ChatGPT 回调 URL 后回车: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			fmt.Fprintf(os.Stderr, "读取失败: %v\n", err)
			return 1
		}
		cb = strings.TrimSpace(line)
	}
	cb = strings.TrimSpace(cb)
	if cb == "" || !strings.HasPrefix(cb, "https://") {
		fmt.Fprintln(os.Stderr, "错误: 回调 URL 必须以 https:// 开头")
		fmt.Fprintln(os.Stderr, "用法: mcpx oauth-register 'https://chatgpt.com/connector/oauth/…'")
		return 1
	}

	origin := strings.TrimRight(strings.TrimSpace(*base), "/")
	if origin == "" {
		origin = defaultOAuthBase()
	}

	payload := map[string]any{
		"client_name":                *name,
		"redirect_uris":              []string{cb},
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
	}
	raw, _ := json.Marshal(payload)

	fmt.Fprintf(os.Stderr, "回调: %s\n", cb)
	fmt.Fprintf(os.Stderr, "注册: %s/mcp/oauth/register\n", origin)

	out, status, err := postRegister(origin+"/mcp/oauth/register", raw)
	if err != nil || status >= 400 {
		// fallback local
		local := localOAuthBase()
		if local != origin {
			fmt.Fprintf(os.Stderr, "公网/指定 base 失败 (%v, status=%d)，改试 %s …\n", err, status, local)
			out, status, err = postRegister(local+"/mcp/oauth/register", raw)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "注册失败: %v\n", err)
		return 1
	}
	if status >= 400 {
		fmt.Fprintf(os.Stderr, "注册失败 HTTP %d: %s\n", status, out)
		return 1
	}

	var pretty bytes.Buffer
	if json.Indent(&pretty, out, "", "  ") == nil {
		fmt.Println(pretty.String())
	} else {
		fmt.Println(string(out))
	}

	var resp map[string]any
	_ = json.Unmarshal(out, &resp)
	cid, _ := resp["client_id"].(string)
	if cid == "" {
		fmt.Fprintln(os.Stderr, "响应中无 client_id")
		return 1
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "======== 填到 ChatGPT ========")
	fmt.Fprintf(os.Stderr, "OAuth 客户端 ID:  %s\n", cid)
	fmt.Fprintln(os.Stderr, "客户端密钥:      （留空）")
	fmt.Fprintln(os.Stderr, "令牌端点认证:    none")
	fmt.Fprintln(os.Stderr, "作用域:          mcp")
	if p := oauthPasswordHint(); p != "" {
		fmt.Fprintf(os.Stderr, "授权口令:        %s\n", p)
	} else {
		fmt.Fprintln(os.Stderr, "授权口令:        见 ~/.mcpx/config.yaml → auth.oauth.password")
	}
	fmt.Fprintln(os.Stderr, "==============================")
	// also print bare client_id on stdout last line for scripts? already printed JSON.
	// macOS clipboard
	copyToClipboard(cid)
	return 0
}

func postRegister(url string, body []byte) ([]byte, int, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return b, res.StatusCode, nil
}

func loadGlobalConfig() (config.Config, error) {
	path, err := config.GlobalConfigPath()
	if err != nil {
		return config.DefaultConfig(), err
	}
	return config.LoadGlobal(path)
}

func defaultOAuthBase() string {
	cfg, err := loadGlobalConfig()
	if err == nil {
		if u := strings.TrimSpace(cfg.Auth.OAuth.ServerURL); u != "" {
			return strings.TrimRight(u, "/")
		}
		return "http://" + cfg.Addr()
	}
	return localOAuthBase()
}

func localOAuthBase() string {
	cfg, err := loadGlobalConfig()
	if err == nil {
		host := cfg.Server.Host
		if host == "0.0.0.0" || host == "::" || host == "" {
			host = "127.0.0.1"
		}
		port := cfg.Server.Port
		if port == 0 {
			port = 9090
		}
		return fmt.Sprintf("http://%s:%d", host, port)
	}
	return "http://127.0.0.1:29090"
}

func oauthPasswordHint() string {
	cfg, err := loadGlobalConfig()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.Auth.OAuth.Password)
}

func copyToClipboard(s string) {
	// best-effort; ignore errors (Linux without display, etc.)
	cmd := clipboardCommand(s)
	if cmd == nil {
		return
	}
	if err := cmd.Run(); err == nil {
		fmt.Fprintln(os.Stderr, "（client_id 已复制到剪贴板）")
	}
}
