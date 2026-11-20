package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
)

const pluginName = "session-manager-plugin"

// findPlugin は session-manager-plugin の場所を返す。
// ECS Exec の端末のやり取り（WebSocket）はこのプラグインが担う。aws CLI も同じものを呼んでいる。
func findPlugin() (string, error) {
	p, err := exec.LookPath(pluginName)
	if err != nil {
		return "", fmt.Errorf("%s が見つかりません。インストールしてください: "+
			"https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html", pluginName)
	}
	return p, nil
}

// wrapCommand は任意のシェル文字列を、イメージに入れた hashi-run に base64 で渡す形にする。
// ECS Exec のコマンドはシェルを通らず、引用符の扱いも限られるため、文字列をそのまま渡すと壊れやすい。
func wrapCommand(shell string) string {
	return "hashi-run " + base64.StdEncoding.EncodeToString([]byte(shell))
}

// shellQuote は 1 つの引数をシェルの単一引用符で囲む。
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '.' || r == '/' || r == '@' || r == ':' || r == '=' || r == ',' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// openSession は ECS Exec のセッションを開き、session-manager-plugin で端末をつなぐ。
// プラグインが終わる（コンテナ内のコマンドが終わる、または exit する）まで戻らない。
func (c *client) openSession(ctx context.Context, plugin, taskArn, runtimeID, command string) error {
	out, err := c.ecs.ExecuteCommand(ctx, &ecs.ExecuteCommandInput{
		Cluster:     aws.String(c.env.Cluster),
		Task:        aws.String(taskArn),
		Container:   aws.String(c.env.Container),
		Interactive: true,
		Command:     aws.String(command),
	})
	if err != nil {
		return fmt.Errorf("ECS Exec を開始できません: %w", err)
	}
	session, err := json.Marshal(map[string]string{
		"SessionId":  aws.ToString(out.Session.SessionId),
		"StreamUrl":  aws.ToString(out.Session.StreamUrl),
		"TokenValue": aws.ToString(out.Session.TokenValue),
	})
	if err != nil {
		return err
	}
	target, err := json.Marshal(map[string]string{
		"Target": fmt.Sprintf("ecs:%s_%s_%s", clusterName(aws.ToString(out.ClusterArn)), taskID(taskArn), runtimeID),
	})
	if err != nil {
		return err
	}

	// 引数の並びは aws ecs execute-command がプラグインを呼ぶときと同じ。
	cmd := exec.Command(plugin, string(session), c.region, "StartSession", c.profileName(), string(target), c.ecsEndpoint())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	// Ctrl+C はプラグインがコンテナ側へ送るので、hashi 自身は無視する。
	// 端末を閉じた・kill されたときは、プラグインを止めて戻る（呼び出し元がタスクを止める）。
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sig)

	restore := disableFlowControl()
	defer restore()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s を起動できません: %w", pluginName, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case err := <-done:
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return fmt.Errorf("%s が終了コード %d で終わりました", pluginName, ee.ExitCode())
			}
			return err
		case s := <-sig:
			if s == os.Interrupt {
				continue
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				_ = cmd.Process.Kill()
			}
		}
	}
}

// disableFlowControl は手元の端末の XON/XOFF（Ctrl+S で出力を止め、Ctrl+Q で再開する端末の機能）を切り、
// 元に戻す関数を返す。
// session-manager-plugin（Linux / Mac 版）は端末を `stty cbreak -echo` にするだけで XON/XOFF を残すため、
// Ctrl+S が手元の端末に取られて接続先へ届かない（接続先のアプリで Ctrl+S が効かず、画面も止まる）。
// Windows 版のプラグインは端末の設定に stty を使わないので、何もしない。
func disableFlowControl() func() {
	if runtime.GOOS == "windows" {
		return func() {}
	}
	var saved bytes.Buffer
	get := exec.Command("stty", "-g")
	get.Stdin, get.Stdout = os.Stdin, &saved
	if err := get.Run(); err != nil {
		return func() {} // 端末でない（パイプなど）か stty が無い
	}
	set := exec.Command("stty", "-ixon")
	set.Stdin = os.Stdin
	if err := set.Run(); err != nil {
		return func() {}
	}
	return func() {
		r := exec.Command("stty", strings.TrimSpace(saved.String()))
		r.Stdin = os.Stdin
		_ = r.Run()
	}
}

func (c *client) ecsEndpoint() string {
	suffix := "amazonaws.com"
	if strings.HasPrefix(c.region, "cn-") {
		suffix = "amazonaws.com.cn"
	}
	return fmt.Sprintf("https://ecs.%s.%s", c.region, suffix)
}
