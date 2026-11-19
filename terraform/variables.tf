variable "region" {
  description = "作成するリージョン"
  type        = string
  default     = "ap-northeast-1"
}

variable "name" {
  description = "資源の名前（クラスター・タスク定義・ECR リポジトリなどに使う）"
  type        = string
  default     = "hashi"
}

variable "vpc_id" {
  description = "既存の VPC に置く場合だけ指定する。省略（null）なら hashi 専用の VPC を新しく作る"
  type        = string
  default     = null
}

variable "subnet_ids" {
  description = "既存の VPC に置く場合のサブネット。接続先へ届き、443 で AWS の API（SSM・ECR・CloudWatch Logs）へ出られること"
  type        = list(string)
  default     = []
}

variable "assign_public_ip" {
  description = "既存の VPC に置く場合、タスクにパブリック IP を付けるか。専用の VPC を作る場合は常に付ける"
  type        = bool
  default     = false
}

variable "vpc_cidr" {
  description = "専用の VPC を作る場合の CIDR"
  type        = string
  default     = "10.250.0.0/16"
}

variable "az_count" {
  description = "専用の VPC を作る場合のサブネット（AZ）の数"
  type        = number
  default     = 2
}

variable "ssh_target_cidrs" {
  description = "SSH で接続してよい宛先の CIDR"
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "ssh_ports" {
  description = "SSH の宛先ポート"
  type        = list(number)
  default     = [22]
}

variable "extra_egress_rules" {
  description = "SSH 以外に許可する外向きの通信（DB への疎通確認など）"
  type = list(object({
    description = string
    protocol    = string # tcp / udp
    port        = number
    cidrs       = list(string)
  }))
  default = []
}

variable "cpu" {
  description = "タスクの CPU ユニット（256 = 0.25 vCPU）"
  type        = number
  default     = 256
}

variable "memory" {
  description = "タスクのメモリ（MiB）"
  type        = number
  default     = 512
}

variable "cpu_architecture" {
  description = "X86_64 または ARM64。イメージのビルド先と合わせる"
  type        = string
  default     = "X86_64"
}

variable "image_tag" {
  description = "使うイメージのタグ"
  type        = string
  default     = "latest"
}

variable "log_retention_days" {
  description = "ログ（コンテナのログと ECS Exec の操作記録）の保持日数"
  type        = number
  default     = 90
}

variable "default_ttl" {
  description = "hashi の設定に出力する TTL の既定値"
  type        = string
  default     = "1h"
}

variable "tags" {
  description = "全資源に付けるタグ"
  type        = map(string)
  default     = {}
}
