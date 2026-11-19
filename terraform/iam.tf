data "aws_iam_policy_document" "ecs_tasks_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.account_id]
    }
  }
}

# タスク実行ロール: ECS がイメージを取得し、ログを書くためのロール
resource "aws_iam_role" "execution" {
  name               = "${var.name}-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json
}

resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:${local.partition}:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

# タスクロール: コンテナの中の権限。ECS Exec の通信路と、操作記録の書き込みだけを許す。
# AWS の他の資源には触れない（踏み台の中から AWS API を叩けないようにする）
resource "aws_iam_role" "task" {
  name               = "${var.name}-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json
}

data "aws_iam_policy_document" "task" {
  statement {
    sid = "EcsExecChannel"
    actions = [
      "ssmmessages:CreateControlChannel",
      "ssmmessages:CreateDataChannel",
      "ssmmessages:OpenControlChannel",
      "ssmmessages:OpenDataChannel",
    ]
    resources = ["*"]
  }
  statement {
    sid       = "EcsExecLogging"
    actions   = ["logs:CreateLogStream", "logs:DescribeLogStreams", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.exec.arn}:*"]
  }
  statement {
    sid       = "EcsExecLoggingDescribe"
    actions   = ["logs:DescribeLogGroups"]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "task" {
  name   = "ecs-exec"
  role   = aws_iam_role.task.id
  policy = data.aws_iam_policy_document.task.json
}

# hashi を使う人に付けるポリシー（SSO の許可セットや IAM ユーザー・グループに付ける）
data "aws_iam_policy_document" "user" {
  statement {
    sid       = "RunTask"
    actions   = ["ecs:RunTask"]
    resources = ["arn:${local.partition}:ecs:${var.region}:${local.account_id}:task-definition/${aws_ecs_task_definition.this.family}:*"]
    condition {
      test     = "ArnEquals"
      variable = "ecs:cluster"
      values   = [aws_ecs_cluster.this.arn]
    }
  }
  statement {
    sid       = "TagOnRunTask"
    actions   = ["ecs:TagResource"]
    resources = ["arn:${local.partition}:ecs:${var.region}:${local.account_id}:task/${aws_ecs_cluster.this.name}/*"]
    condition {
      test     = "StringEquals"
      variable = "ecs:CreateAction"
      values   = ["RunTask"]
    }
  }
  statement {
    sid     = "ManageTasks"
    actions = ["ecs:DescribeTasks", "ecs:StopTask", "ecs:ExecuteCommand"]
    resources = [
      aws_ecs_cluster.this.arn,
      "arn:${local.partition}:ecs:${var.region}:${local.account_id}:task/${aws_ecs_cluster.this.name}/*",
    ]
  }
  statement {
    sid       = "ListTasks"
    actions   = ["ecs:ListTasks"]
    resources = ["*"]
    condition {
      test     = "ArnEquals"
      variable = "ecs:cluster"
      values   = [aws_ecs_cluster.this.arn]
    }
  }
  statement {
    sid       = "PassRoles"
    actions   = ["iam:PassRole"]
    resources = [aws_iam_role.execution.arn, aws_iam_role.task.arn]
    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_policy" "user" {
  name        = "${var.name}-user"
  description = "hashi で一時タスクを起動して入るための権限"
  policy      = data.aws_iam_policy_document.user.json
}
