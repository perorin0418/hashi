// hashi は、一時的な ECS Fargate タスクを起動して ECS Exec で中に入り、
// 踏み台として使い終わったらタスクを止めるコマンド。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

var version = "dev" // ビルド時に -ldflags で埋める

const usage = `hashi — 一時的な ECS タスクを踏み台にするコマンド

使い方:
  hashi [-e 接続先] [-c 設定ファイル] <サブコマンド> [オプション]

サブコマンド:
  start                 タスクを起動してシェルに入る。抜けるとタスクを止める
  ssh [ssh の引数...]   タスクを起動して ssh を実行する。終わるとタスクを止める
  exec <コマンド...>    タスクを起動してコマンドを実行する。終わるとタスクを止める
  attach <タスクID>     動いているタスクに入る（止めない）
  ls [--all]            hashi が起動した動いているタスクを一覧する（既定は自分のものだけ）
  stop <タスクID>...    タスクを止める（--mine で自分のものをすべて止める）
  envs                  設定ファイルの接続先を一覧する
  version               バージョンを表示する

start / ssh / exec の共通オプション:
  --ttl 2h    タスクの最長寿命（既定は設定の ttl、なければ 1h、上限 12h）。手元が落ちてもこの時間で止まる
  --keep      抜けてもタスクを止めない（あとで attach / stop する）

例:
  hashi -e dev start
  hashi -e dev ssh ec2-user@10.0.1.23
  hashi -e dev exec 'nc -zv 10.0.1.23 5432'
  hashi -e dev ssh --ttl 3h -- -p 2222 user@10.0.1.23   （- で始まる引数を渡すときは -- で区切る）
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hashi:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	gf := flag.NewFlagSet("hashi", flag.ContinueOnError)
	gf.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	envFlag := gf.String("e", "", "接続先（設定ファイルの envs のキー）")
	cfgFlag := gf.String("c", "", "設定ファイルのパス")
	if err := gf.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if gf.NArg() == 0 {
		gf.Usage()
		return errors.New("サブコマンドを指定してください")
	}
	sub, rest := gf.Arg(0), gf.Args()[1:]

	switch sub {
	case "version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		gf.Usage()
		return nil
	}

	path, err := configPath(*cfgFlag)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return err
	}
	if sub == "envs" {
		for _, n := range cfg.envNames() {
			mark := " "
			if n == cfg.DefaultEnv {
				mark = "*"
			}
			e := cfg.Envs[n]
			fmt.Printf("%s %-12s profile=%s region=%s cluster=%s\n", mark, n, e.Profile, e.Region, e.Cluster)
		}
		return nil
	}

	envName, env, err := cfg.selectEnv(*envFlag)
	if err != nil {
		return err
	}
	ctx := context.Background()
	c, err := newClient(ctx, envName, env)
	if err != nil {
		return err
	}

	switch sub {
	case "start":
		return cmdLaunch(ctx, c, "start", rest, func([]string) (string, error) { return env.Shell, nil })
	case "ssh":
		return cmdLaunch(ctx, c, "ssh", rest, func(a []string) (string, error) {
			if len(a) == 0 {
				return "", errors.New("ssh の接続先を指定してください（例: hashi ssh user@host）")
			}
			q := make([]string, len(a))
			for i, s := range a {
				q[i] = shellQuote(s)
			}
			return wrapCommand("ssh " + strings.Join(q, " ")), nil
		})
	case "exec":
		return cmdLaunch(ctx, c, "exec", rest, func(a []string) (string, error) {
			if len(a) == 0 {
				return "", errors.New("実行するコマンドを指定してください")
			}
			// ssh のリモートコマンドと同じく、引数を空白でつないだ文字列をシェルで解釈する
			return wrapCommand(strings.Join(a, " ")), nil
		})
	case "attach":
		return cmdAttach(ctx, c, rest)
	case "ls":
		return cmdList(ctx, c, rest)
	case "stop":
		return cmdStop(ctx, c, rest)
	default:
		gf.Usage()
		return fmt.Errorf("不明なサブコマンドです: %s", sub)
	}
}

// cmdLaunch は start / ssh / exec の共通処理。
// 起動 → 準備待ち → セッション → 停止。途中で失敗・中断しても、起動したタスクは止める。
func cmdLaunch(ctx context.Context, c *client, name string, args []string, build func([]string) (string, error)) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	ttlFlag := fs.Duration("ttl", 0, "タスクの最長寿命")
	keep := fs.Bool("keep", false, "抜けてもタスクを止めない")
	if err := fs.Parse(args); err != nil {
		return err
	}
	command, err := build(fs.Args())
	if err != nil {
		return err
	}
	ttl, err := resolveTTL(*ttlFlag, c.env)
	if err != nil {
		return err
	}
	plugin, err := findPlugin()
	if err != nil {
		return err
	}
	owner, err := c.callerARN(ctx)
	if err != nil {
		return err
	}

	// 準備待ちの間の Ctrl+C は「やめる」の意味なので、待ちを打ち切ってタスクを止める。
	waitCtx, stopNotify := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stopNotify()

	fmt.Fprintf(os.Stderr, "[%s] タスクを起動します（TTL %s）\n", c.envName, ttl)
	taskArn, err := c.runTask(waitCtx, owner, ttl)
	if err != nil {
		return err
	}
	id := taskID(taskArn)
	fmt.Fprintf(os.Stderr, "  タスク ID: %s\n", id)

	stopped := false
	defer func() {
		if *keep || stopped {
			return
		}
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.stopTask(sctx, taskArn, "hashi: session ended"); err != nil {
			fmt.Fprintf(os.Stderr, "タスクを止められませんでした。手で止めてください: hashi -e %s stop %s（%v）\n", c.envName, id, err)
			return
		}
		fmt.Fprintf(os.Stderr, "[%s] タスクを止めました: %s\n", c.envName, id)
	}()

	_, runtimeID, err := c.waitReady(waitCtx, taskArn)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return errors.New("中断しました")
		}
		if errors.Is(err, errTaskStopped) {
			stopped = true
		}
		return err
	}
	stopNotify() // ここからの Ctrl+C はコンテナ側へ送る

	fmt.Fprintf(os.Stderr, "[%s] 接続します（抜けるには exit）\n", c.envName)
	if *keep {
		defer fmt.Fprintf(os.Stderr, "タスクは動いたままです。入る: hashi -e %s attach %s / 止める: hashi -e %s stop %s\n", c.envName, id, c.envName, id)
	}
	return c.openSession(ctx, plugin, taskArn, runtimeID, command)
}

func cmdAttach(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("タスク ID を 1 つ指定してください（hashi ls で確認できます）")
	}
	plugin, err := findPlugin()
	if err != nil {
		return err
	}
	t, err := c.describeTask(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	if s := aws.ToString(t.LastStatus); s != "RUNNING" {
		return fmt.Errorf("タスクが動いていません（状態: %s）", s)
	}
	ct := findContainer(t, c.env.Container)
	if ct == nil || aws.ToString(ct.RuntimeId) == "" {
		return fmt.Errorf("コンテナ %s が見つかりません", c.env.Container)
	}
	return c.openSession(ctx, plugin, aws.ToString(t.TaskArn), aws.ToString(ct.RuntimeId), c.env.Shell)
}

func cmdList(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	all := fs.Bool("all", false, "他の人のタスクも表示する")
	if err := fs.Parse(args); err != nil {
		return err
	}
	owner, err := c.callerARN(ctx)
	if err != nil {
		return err
	}
	tasks, err := c.listTasks(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TASK_ID\tSTATUS\tSTARTED\tEXPIRES\tOWNER")
	for i := range tasks {
		t := &tasks[i]
		o := tagValue(t, tagOwner)
		if !*all && o != owner {
			continue
		}
		started := "-"
		if t.StartedAt != nil {
			started = t.StartedAt.Local().Format("01-02 15:04")
		}
		expires := "-"
		if e, err := time.Parse(time.RFC3339, tagValue(t, tagExpiresAt)); err == nil {
			expires = e.Local().Format("01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", taskID(aws.ToString(t.TaskArn)), aws.ToString(t.LastStatus), started, expires, shortOwner(o))
	}
	return w.Flush()
}

func cmdStop(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	mine := fs.Bool("mine", false, "自分が起動したタスクをすべて止める")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ids := fs.Args()
	if *mine {
		owner, err := c.callerARN(ctx)
		if err != nil {
			return err
		}
		tasks, err := c.listTasks(ctx)
		if err != nil {
			return err
		}
		for i := range tasks {
			if tagValue(&tasks[i], tagOwner) == owner {
				ids = append(ids, taskID(aws.ToString(tasks[i].TaskArn)))
			}
		}
		if len(ids) == 0 {
			fmt.Fprintln(os.Stderr, "止めるタスクはありません")
			return nil
		}
	}
	if len(ids) == 0 {
		return errors.New("タスク ID を指定するか --mine を付けてください")
	}
	var failed int
	for _, id := range ids {
		if err := c.stopTask(ctx, id, "hashi: stopped by user"); err != nil {
			fmt.Fprintf(os.Stderr, "止められませんでした: %s: %v\n", id, err)
			failed++
			continue
		}
		fmt.Fprintf(os.Stderr, "止めました: %s\n", id)
	}
	if failed > 0 {
		return fmt.Errorf("%d 件を止められませんでした", failed)
	}
	return nil
}

// shortOwner は ARN の末尾（SSO ならユーザー名やメールアドレス）を返す。
func shortOwner(arn string) string {
	if arn == "" {
		return "-"
	}
	return arn[strings.LastIndex(arn, "/")+1:]
}
