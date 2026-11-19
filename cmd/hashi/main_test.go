package main

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShellQuoteRoundTrip(t *testing.T) {
	args := []string{"-p", "2222", "user@10.0.0.1", "echo 'a b' \"$HOME\"; ls | wc -l", ""}
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellQuote(a)
	}
	// bash で展開し直すと元の引数に戻ること
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash がありません")
	}
	script := `for a in ` + strings.Join(q, " ") + `; do printf '%s\0' "$a"; done`
	got, err := exec.Command("bash", "-c", script).Output()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.TrimSuffix(string(got), "\x00"), "\x00")
	if strings.Join(parts, "|") != strings.Join(args, "|") {
		t.Fatalf("got %q want %q", parts, args)
	}
}

func TestWrapCommandIsSafe(t *testing.T) {
	w := wrapCommand(`ssh u@h "a 'b'"`)
	if !strings.HasPrefix(w, "hashi-run ") {
		t.Fatal(w)
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(w, "hashi-run "))
	if err != nil || string(b) != `ssh u@h "a 'b'"` {
		t.Fatalf("decode: %q %v", b, err)
	}
	if strings.ContainsAny(strings.TrimPrefix(w, "hashi-run "), " '\"") {
		t.Fatal("引用符や空白が入っている")
	}
}

func TestSelectEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(p, []byte(`
default_env: dev
envs:
  dev:
    region: ap-northeast-1
    cluster: hashi
    task_definition: hashi
    subnets: [subnet-1]
    ttl: 30m
  prd:
    cluster: hashi
`), 0o600)
	c, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HASHI_ENV", "")
	name, e, err := c.selectEnv("")
	if err != nil || name != "dev" || e.Container != "hashi" || e.Shell != defaultShell {
		t.Fatalf("%s %+v %v", name, e, err)
	}
	if _, _, err := c.selectEnv("prd"); err == nil || !strings.Contains(err.Error(), "task_definition") {
		t.Fatalf("必須項目の不足を検出すること: %v", err)
	}
	if _, _, err := c.selectEnv("nope"); err == nil {
		t.Fatal("存在しない接続先はエラー")
	}
	if ttl, _ := resolveTTL(0, e); ttl != 30*time.Minute {
		t.Fatal(ttl)
	}
	if ttl, _ := resolveTTL(48*time.Hour, e); ttl != maxTTL {
		t.Fatal(ttl)
	}
}

func TestIDs(t *testing.T) {
	if taskID("arn:aws:ecs:ap-northeast-1:123:task/hashi/abc") != "abc" || taskID("abc") != "abc" {
		t.Fatal("taskID")
	}
	if clusterName("arn:aws:ecs:ap-northeast-1:123:cluster/hashi") != "hashi" {
		t.Fatal("clusterName")
	}
}
