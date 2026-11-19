output "ecr_repository_url" {
  description = "イメージの push 先"
  value       = aws_ecr_repository.this.repository_url
}

output "user_policy_arn" {
  description = "hashi を使う人に付けるポリシー"
  value       = aws_iam_policy.user.arn
}

output "exec_log_group" {
  description = "ECS Exec の操作記録のロググループ"
  value       = aws_cloudwatch_log_group.exec.name
}

# hashi の設定ファイル（envs の 1 項目）にそのまま貼れる形
output "hashi_env_config" {
  description = "hashi の設定ファイルの envs.<接続先> に貼る内容（profile は各自で足す）"
  value = yamlencode({
    region           = var.region
    cluster          = aws_ecs_cluster.this.name
    task_definition  = aws_ecs_task_definition.this.family
    container        = "hashi"
    subnets          = local.subnet_ids
    security_groups  = [aws_security_group.task.id]
    assign_public_ip = local.assign_public_ip
    ttl              = var.default_ttl
  })
}
