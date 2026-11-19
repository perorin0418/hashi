package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Env は接続先 1 つ分（AWS アカウント・リージョンごと）の設定。
// 値の多くは terraform の出力 hashi_env_config をそのまま貼り付けて使う。
type Env struct {
	Profile        string   `yaml:"profile"`          // AWS プロファイル名。空なら AWS_PROFILE などの既定に従う
	Region         string   `yaml:"region"`           // 空ならプロファイルの既定リージョン
	Cluster        string   `yaml:"cluster"`          // ECS クラスター名
	TaskDefinition string   `yaml:"task_definition"`  // タスク定義のファミリー名（リビジョン指定も可）
	Container      string   `yaml:"container"`        // コンテナ名。既定 hashi
	Subnets        []string `yaml:"subnets"`          // タスクを置くサブネット
	SecurityGroups []string `yaml:"security_groups"`  // タスクに付けるセキュリティグループ
	AssignPublicIP bool     `yaml:"assign_public_ip"` // パブリック IP を付けるか
	FargateSpot    bool     `yaml:"fargate_spot"`     // Fargate Spot で起動するか（安いが中断されうる）
	TTL            string   `yaml:"ttl"`              // タスクの最長寿命。既定 1h
	Shell          string   `yaml:"shell"`            // 入ったときのシェル。既定 /bin/bash -l
}

type Config struct {
	DefaultEnv string         `yaml:"default_env"`
	Envs       map[string]Env `yaml:"envs"`
}

const (
	defaultContainer = "hashi"
	defaultTTL       = time.Hour
	maxTTL           = 12 * time.Hour
	defaultShell     = "/bin/bash -l"
)

// configPath は設定ファイルの場所を返す。HASHI_CONFIG が最優先。
func configPath(flagPath string) (string, error) {
	if flagPath != "" {
		return flagPath, nil
	}
	if p := os.Getenv("HASHI_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hashi", "config.yaml"), nil
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("設定ファイルがありません: %s（README の「設定ファイル」を参照）", path)
		}
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("設定ファイルを読めません: %s: %w", path, err)
	}
	if len(c.Envs) == 0 {
		return nil, fmt.Errorf("設定ファイルに envs がありません: %s", path)
	}
	return &c, nil
}

// selectEnv は -e > HASHI_ENV > default_env > 唯一の env の順で接続先を決める。
func (c *Config) selectEnv(name string) (string, Env, error) {
	if name == "" {
		name = os.Getenv("HASHI_ENV")
	}
	if name == "" {
		name = c.DefaultEnv
	}
	if name == "" && len(c.Envs) == 1 {
		for k := range c.Envs {
			name = k
		}
	}
	if name == "" {
		return "", Env{}, fmt.Errorf("接続先を -e で指定してください（候補: %s）", strings.Join(c.envNames(), ", "))
	}
	e, ok := c.Envs[name]
	if !ok {
		return "", Env{}, fmt.Errorf("接続先 %q は設定にありません（候補: %s）", name, strings.Join(c.envNames(), ", "))
	}
	if e.Container == "" {
		e.Container = defaultContainer
	}
	if e.Shell == "" {
		e.Shell = defaultShell
	}
	var missing []string
	if e.Cluster == "" {
		missing = append(missing, "cluster")
	}
	if e.TaskDefinition == "" {
		missing = append(missing, "task_definition")
	}
	if len(e.Subnets) == 0 {
		missing = append(missing, "subnets")
	}
	if len(missing) > 0 {
		return "", Env{}, fmt.Errorf("接続先 %q に必須項目がありません: %s", name, strings.Join(missing, ", "))
	}
	return name, e, nil
}

func (c *Config) envNames() []string {
	names := make([]string, 0, len(c.Envs))
	for k := range c.Envs {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// resolveTTL はフラグ > 設定 > 既定の順で TTL を決め、上限で丸める。
func resolveTTL(flagTTL time.Duration, e Env) (time.Duration, error) {
	ttl := flagTTL
	if ttl == 0 && e.TTL != "" {
		d, err := time.ParseDuration(e.TTL)
		if err != nil {
			return 0, fmt.Errorf("設定の ttl を解釈できません: %q", e.TTL)
		}
		ttl = d
	}
	if ttl == 0 {
		ttl = defaultTTL
	}
	if ttl < time.Minute {
		return 0, fmt.Errorf("ttl は 1 分以上にしてください: %s", ttl)
	}
	if ttl > maxTTL {
		fmt.Fprintf(os.Stderr, "ttl %s は上限 %s に丸めます\n", ttl, maxTTL)
		ttl = maxTTL
	}
	return ttl, nil
}
