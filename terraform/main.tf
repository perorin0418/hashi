data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}

locals {
  account_id = data.aws_caller_identity.current.account_id
  partition  = data.aws_partition.current.partition
}

# ---------------------------------------------------------------
# コンテナイメージ
# ---------------------------------------------------------------
resource "aws_ecr_repository" "this" {
  name                 = var.name
  image_tag_mutability = "MUTABLE"
  image_scanning_configuration {
    scan_on_push = true
  }
}

resource "aws_ecr_lifecycle_policy" "this" {
  repository = aws_ecr_repository.this.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "古いイメージを 10 個残して消す"
      selection    = { tagStatus = "any", countType = "imageCountMoreThan", countNumber = 10 }
      action       = { type = "expire" }
    }]
  })
}

# ---------------------------------------------------------------
# ログ
# ---------------------------------------------------------------
resource "aws_cloudwatch_log_group" "container" {
  name              = "/hashi/${var.name}/container"
  retention_in_days = var.log_retention_days
}

# ECS Exec のセッションの入出力の記録（誰が何をしたかの監査用）
resource "aws_cloudwatch_log_group" "exec" {
  name              = "/hashi/${var.name}/exec"
  retention_in_days = var.log_retention_days
}

# ---------------------------------------------------------------
# クラスター
# ---------------------------------------------------------------
resource "aws_ecs_cluster" "this" {
  name = var.name

  configuration {
    execute_command_configuration {
      logging = "OVERRIDE"
      log_configuration {
        cloud_watch_log_group_name     = aws_cloudwatch_log_group.exec.name
        cloud_watch_encryption_enabled = false
      }
    }
  }
}

resource "aws_ecs_cluster_capacity_providers" "this" {
  cluster_name       = aws_ecs_cluster.this.name
  capacity_providers = ["FARGATE", "FARGATE_SPOT"]
}

# ---------------------------------------------------------------
# ネットワーク
# 受信は一切許可しない（ECS Exec は外向きの 443 だけで成り立つ）
# ---------------------------------------------------------------
resource "aws_security_group" "task" {
  name        = "${var.name}-task"
  description = "hashi bastion task"
  vpc_id      = local.vpc_id
}

resource "aws_vpc_security_group_egress_rule" "https" {
  security_group_id = aws_security_group.task.id
  description       = "AWS API (SSM, ECR, CloudWatch Logs)"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv4         = "0.0.0.0/0"
}

locals {
  ssh_rules = flatten([
    for port in var.ssh_ports : [
      for cidr in var.ssh_target_cidrs : { key = "ssh-${port}-${cidr}", description = "SSH", protocol = "tcp", port = port, cidr = cidr }
    ]
  ])
  extra_rules = flatten([
    for r in var.extra_egress_rules : [
      for cidr in r.cidrs : { key = "${r.protocol}-${r.port}-${cidr}", description = r.description, protocol = r.protocol, port = r.port, cidr = cidr }
    ]
  ])
}

resource "aws_vpc_security_group_egress_rule" "targets" {
  for_each          = { for r in concat(local.ssh_rules, local.extra_rules) : r.key => r }
  security_group_id = aws_security_group.task.id
  description       = each.value.description
  ip_protocol       = each.value.protocol
  from_port         = each.value.port
  to_port           = each.value.port
  cidr_ipv4         = each.value.cidr
}

# ---------------------------------------------------------------
# タスク定義
# ---------------------------------------------------------------
resource "aws_ecs_task_definition" "this" {
  family                   = var.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.cpu
  memory                   = var.memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.task.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = var.cpu_architecture
  }

  container_definitions = jsonencode([{
    name      = "hashi"
    image     = "${aws_ecr_repository.this.repository_url}:${var.image_tag}"
    essential = true
    command   = ["sleep", "3600"]
    # ECS Exec で起動したプロセスの後始末のため init を有効にする（AWS の推奨）
    linuxParameters = { initProcessEnabled = true }
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.container.name
        awslogs-region        = var.region
        awslogs-stream-prefix = "hashi"
      }
    }
  }])
}
