package panelmigration

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"regexp"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
)

//go:embed remote.py
var remoteProgram string

type Target struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Fingerprint string `json:"fingerprint"`
}
type Credentials struct {
	Password   string `json:"ssh_password"`
	PrivateKey string `json:"ssh_private_key"`
	Passphrase string `json:"ssh_key_passphrase"`
}
type RemoteInput struct {
	Action                string         `json:"action"`
	ID                    string         `json:"id"`
	Version               string         `json:"version"`
	Domain                string         `json:"domain"`
	PublicIP              string         `json:"public_ip"`
	SourceURL             string         `json:"source_url"`
	Username              string         `json:"username"`
	AdministratorPassword string         `json:"administrator_password,omitempty"`
	Password              string         `json:"password,omitempty"`
	BootstrapPassword     string         `json:"bootstrap_password,omitempty"`
	Installer             string         `json:"installer,omitempty"`
	Archive               []byte         `json:"archive,omitempty"`
	Digest                string         `json:"digest,omitempty"`
	Counts                map[string]int `json:"counts,omitempty"`
}
type RemoteResult struct {
	State      string `json:"state"`
	Code       string `json:"code"`
	RecoveryID string `json:"recovery_id,omitempty"`
}
type Transport interface {
	Probe(context.Context, Target) (string, error)
	Run(context.Context, Target, Credentials, RemoteInput) (RemoteResult, error)
}
type SSHTransport struct{}

var fingerprintPattern = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

func dialSSH(ctx context.Context, target Target, auth []ssh.AuthMethod, callback ssh.HostKeyCallback) (*ssh.Client, error) {
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(target.Host, strconv.Itoa(target.Port)))
	if err != nil {
		return nil, errors.New("SSH 连接失败，请核对地址、端口及防火墙")
	}
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	config := &ssh.ClientConfig{User: "root", Auth: auth, HostKeyCallback: callback, Timeout: 15 * time.Second}
	c, channels, requests, err := ssh.NewClientConn(conn, target.Host, config)
	if err != nil {
		conn.Close()
		return nil, errors.New("SSH 握手或认证失败，请核对凭据和服务器指纹")
	}
	_ = conn.SetDeadline(time.Time{})
	if ctx.Err() != nil {
		_ = c.Close()
		return nil, ctx.Err()
	}
	return ssh.NewClient(c, channels, requests), nil
}
func (SSHTransport) Probe(ctx context.Context, target Target) (string, error) {
	var fingerprint string
	client, err := dialSSH(ctx, target, nil, func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fingerprint = ssh.FingerprintSHA256(key)
		return errors.New("host key obtained without sending credentials")
	})
	if client != nil {
		client.Close()
	}
	if fingerprint != "" {
		return fingerprint, nil
	}
	return "", err
}

func (SSHTransport) Run(ctx context.Context, target Target, credentials Credentials, input RemoteInput) (RemoteResult, error) {
	var methods []ssh.AuthMethod
	if credentials.PrivateKey != "" {
		var signer ssh.Signer
		var err error
		if credentials.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(credentials.PrivateKey), []byte(credentials.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(credentials.PrivateKey))
		}
		if err != nil {
			return RemoteResult{}, errors.New("SSH 私钥格式或口令无效")
		}
		methods = append(methods, ssh.PublicKeys(signer))
	} else if credentials.Password != "" {
		methods = append(methods, ssh.Password(credentials.Password))
	} else {
		return RemoteResult{}, errors.New("请填写 SSH 密码或私钥")
	}
	client, err := dialSSH(ctx, target, methods, func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if ssh.FingerprintSHA256(key) != target.Fingerprint {
			return errors.New("host key changed")
		}
		return nil
	})
	if err != nil {
		return RemoteResult{}, err
	}
	defer client.Close()
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			client.Close()
		case <-finished:
		}
	}()
	session, err := client.NewSession()
	if err != nil {
		return RemoteResult{}, errors.New("无法打开迁移 SSH 会话")
	}
	defer session.Close()
	raw, err := json.Marshal(input)
	if err != nil {
		return RemoteResult{}, errors.New("无法准备迁移请求")
	}
	defer clear(raw)
	session.Stdin = bytes.NewReader(raw)
	var output limitedOutput
	session.Stdout = &output
	session.Stderr = io.Discard
	// Only the compiled migration executor is run; all input travels over stdin.
	command := "command -v python3 >/dev/null 2>&1 || (apt-get update >/dev/null 2>&1 && apt-get install -y python3 >/dev/null 2>&1); exec python3 -c " + shellLiteral(remoteProgram)
	err = session.Run(command)
	var result RemoteResult
	if json.Unmarshal(output.Bytes(), &result) != nil {
		return result, errors.New("迁移执行器中断或响应无效；可重新输入 SSH 凭据重试")
	}
	if result.Code != "" {
		return result, remoteError(result.Code)
	}
	if err != nil {
		return result, errors.New("迁移执行器未正常结束；请查看任务阶段并重试")
	}
	return result, nil
}
func shellLiteral(value string) string {
	var b bytes.Buffer
	b.WriteByte('\'')
	for _, c := range value {
		if c == '\'' {
			b.WriteString("'\"'\"'")
		} else {
			b.WriteRune(c)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 65536 {
		return 0, errors.New("migration response too large")
	}
	return b.Buffer.Write(p)
}
func remoteError(code string) error {
	messages := map[string]string{
		"unsupported": "新机须为 Debian/Ubuntu、amd64，并允许 root SSH 登录",
		"occupied":    "新机已有面板或其他迁移任务；本功能只安装到全新服务器，不覆盖已有业务",
		"busy":        "新机仍有迁移操作运行，请稍后重试",
		"install":     "新机安装失败。安装器已尝试回滚；请检查新机软件源、端口或 GitHub 下载连接后重试",
		"version":     "新机版本与当前面板版本不一致；请先发布并安装同一正式版本",
		"restore":     "新机恢复未完成；旧机仍保持隔离，重新输入凭据可核对恢复记录并重试",
		"counts":      "新机数据数量与最终备份不一致，已阻止切换",
		"tls":         "域名证书尚未就绪，请检查 DNS、80/443 端口和证书申请条件后重试",
		"health":      "新面板健康检查未通过，尚未确认迁移完成",
		"marker":      "新机迁移任务标识不匹配，已阻止操作",
		"invalid":     "迁移执行器收到无效参数",
		"internal":    "新机迁移执行失败；保留现有数据，请核对任务阶段后重试",
	}
	if message := messages[code]; message != "" {
		return &remoteFailure{message}
	}
	return &remoteFailure{"新机迁移执行失败"}
}

type remoteFailure struct{ message string }

func (e *remoteFailure) Error() string { return e.message }
