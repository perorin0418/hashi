package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

const (
	startedBy    = "hashi" // ListTasks の絞り込みに使う（36 文字以内）
	tagOwner     = "hashi:owner"
	tagExpiresAt = "hashi:expires-at"
	readyTimeout = 5 * time.Minute
	pollInterval = 3 * time.Second
)

// errTaskStopped は準備待ちの間にタスクが自分で止まったことを表す。
var errTaskStopped = errors.New("タスクが止まりました")

type client struct {
	envName string
	env     Env
	region  string
	ecs     *ecs.Client
	sts     *sts.Client
}

func newClient(ctx context.Context, envName string, e Env) (*client, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if e.Profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(e.Profile))
	}
	if e.Region != "" {
		opts = append(opts, awsconfig.WithRegion(e.Region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("AWS の設定を読めません: %w", err)
	}
	if cfg.Region == "" {
		return nil, errors.New("リージョンが決まりません。設定ファイルの region かプロファイルで指定してください")
	}
	return &client{
		envName: envName,
		env:     e,
		region:  cfg.Region,
		ecs:     ecs.NewFromConfig(cfg),
		sts:     sts.NewFromConfig(cfg),
	}, nil
}

// profileName は session-manager-plugin に渡すプロファイル名。
func (c *client) profileName() string {
	if c.env.Profile != "" {
		return c.env.Profile
	}
	return os.Getenv("AWS_PROFILE")
}

func (c *client) callerARN(ctx context.Context) (string, error) {
	out, err := c.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("AWS の認証に失敗しました（SSO なら aws sso login を実行してください）: %w", err)
	}
	return aws.ToString(out.Arn), nil
}

// runTask はタスクを 1 つ起動し、タスク ARN を返す。
// コンテナのコマンドを sleep <TTL> に上書きするので、手元が落ちても TTL で必ず止まる。
func (c *client) runTask(ctx context.Context, owner string, ttl time.Duration) (string, error) {
	expires := time.Now().Add(ttl).UTC().Format(time.RFC3339)
	assign := types.AssignPublicIpDisabled
	if c.env.AssignPublicIP {
		assign = types.AssignPublicIpEnabled
	}
	in := &ecs.RunTaskInput{
		Cluster:              aws.String(c.env.Cluster),
		TaskDefinition:       aws.String(c.env.TaskDefinition),
		Count:                aws.Int32(1),
		EnableExecuteCommand: true,
		StartedBy:            aws.String(startedBy),
		NetworkConfiguration: &types.NetworkConfiguration{
			AwsvpcConfiguration: &types.AwsVpcConfiguration{
				Subnets:        c.env.Subnets,
				SecurityGroups: c.env.SecurityGroups,
				AssignPublicIp: assign,
			},
		},
		Overrides: &types.TaskOverride{
			ContainerOverrides: []types.ContainerOverride{{
				Name:    aws.String(c.env.Container),
				Command: []string{"sleep", strconv.Itoa(int(ttl.Seconds()))},
			}},
		},
		Tags: []types.Tag{
			{Key: aws.String(tagOwner), Value: aws.String(owner)},
			{Key: aws.String(tagExpiresAt), Value: aws.String(expires)},
		},
	}
	if c.env.FargateSpot {
		in.CapacityProviderStrategy = []types.CapacityProviderStrategyItem{{CapacityProvider: aws.String("FARGATE_SPOT"), Weight: 1}}
	} else {
		in.LaunchType = types.LaunchTypeFargate
	}
	out, err := c.ecs.RunTask(ctx, in)
	if err != nil {
		return "", fmt.Errorf("タスクを起動できません: %w", err)
	}
	if len(out.Failures) > 0 {
		f := out.Failures[0]
		return "", fmt.Errorf("タスクを起動できません: %s %s", aws.ToString(f.Reason), aws.ToString(f.Detail))
	}
	if len(out.Tasks) == 0 {
		return "", errors.New("タスクを起動できません: 応答にタスクがありません")
	}
	return aws.ToString(out.Tasks[0].TaskArn), nil
}

func (c *client) describeTask(ctx context.Context, task string) (*types.Task, error) {
	out, err := c.ecs.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(c.env.Cluster),
		Tasks:   []string{task},
		Include: []types.TaskField{types.TaskFieldTags},
	})
	if err != nil {
		return nil, err
	}
	if len(out.Tasks) == 0 {
		return nil, fmt.Errorf("タスクが見つかりません: %s", task)
	}
	return &out.Tasks[0], nil
}

// waitReady はタスクが RUNNING になり、ECS Exec のエージェントが動き出すまで待つ。
// 戻り値はコンテナの runtimeId（session-manager-plugin の接続先の指定に使う）。
func (c *client) waitReady(ctx context.Context, task string) (*types.Task, string, error) {
	ctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	last := ""
	for {
		t, err := c.describeTask(ctx, task)
		if err != nil {
			return nil, "", err
		}
		status := aws.ToString(t.LastStatus)
		if status == "STOPPED" {
			return nil, "", fmt.Errorf("%w: %s", errTaskStopped, stoppedReason(t))
		}
		ct := findContainer(t, c.env.Container)
		agent := "-"
		if ct != nil {
			for _, a := range ct.ManagedAgents {
				if a.Name == types.ManagedAgentNameExecuteCommandAgent {
					agent = aws.ToString(a.LastStatus)
				}
			}
		}
		if s := status + "/" + agent; s != last {
			fmt.Fprintf(os.Stderr, "  状態: %s（ECS Exec エージェント: %s）\n", status, agent)
			last = s
		}
		if status == "RUNNING" && agent == "RUNNING" && ct != nil && aws.ToString(ct.RuntimeId) != "" {
			return t, aws.ToString(ct.RuntimeId), nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, "", fmt.Errorf("%s 待ってもタスクの準備ができませんでした（最後の状態: %s）", readyTimeout, last)
			}
			return nil, "", ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func (c *client) stopTask(ctx context.Context, task, reason string) error {
	_, err := c.ecs.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(c.env.Cluster),
		Task:    aws.String(task),
		Reason:  aws.String(reason),
	})
	return err
}

// listTasks は hashi が起動した動いているタスクを返す。
func (c *client) listTasks(ctx context.Context) ([]types.Task, error) {
	var arns []string
	p := ecs.NewListTasksPaginator(c.ecs, &ecs.ListTasksInput{
		Cluster:       aws.String(c.env.Cluster),
		StartedBy:     aws.String(startedBy),
		DesiredStatus: types.DesiredStatusRunning,
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		arns = append(arns, page.TaskArns...)
	}
	var tasks []types.Task
	for i := 0; i < len(arns); i += 100 { // DescribeTasks は 1 回 100 件まで
		end := min(i+100, len(arns))
		out, err := c.ecs.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(c.env.Cluster),
			Tasks:   arns[i:end],
			Include: []types.TaskField{types.TaskFieldTags},
		})
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, out.Tasks...)
	}
	return tasks, nil
}

func findContainer(t *types.Task, name string) *types.Container {
	for i := range t.Containers {
		if aws.ToString(t.Containers[i].Name) == name {
			return &t.Containers[i]
		}
	}
	return nil
}

func stoppedReason(t *types.Task) string {
	parts := []string{aws.ToString(t.StoppedReason)}
	for _, ct := range t.Containers {
		if r := aws.ToString(ct.Reason); r != "" {
			parts = append(parts, aws.ToString(ct.Name)+": "+r)
		}
	}
	return strings.Join(parts, " / ")
}

func tagValue(t *types.Task, key string) string {
	for _, tg := range t.Tags {
		if aws.ToString(tg.Key) == key {
			return aws.ToString(tg.Value)
		}
	}
	return ""
}

// taskID は ARN の末尾（タスク ID）を返す。ID が渡されたらそのまま返す。
func taskID(arnOrID string) string {
	return arnOrID[strings.LastIndex(arnOrID, "/")+1:]
}

// clusterName は ARN でも名前でもクラスター名を返す。
func clusterName(arnOrName string) string {
	return arnOrName[strings.LastIndex(arnOrName, "/")+1:]
}
