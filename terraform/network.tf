# ---------------------------------------------------------------
# hashi 専用の VPC（var.vpc_id を省略したときだけ作る）
# パブリックサブネットとインターネットゲートウェイだけの最小構成。
# タスクはパブリック IP で直接外へ出る。受信はセキュリティグループですべて拒否する。
# NAT ゲートウェイや VPC エンドポイントは作らないので、常設の費用はかからない。
# ---------------------------------------------------------------
locals {
  create_vpc = var.vpc_id == null
}

data "aws_availability_zones" "available" {
  count = local.create_vpc ? 1 : 0
  state = "available"
}

resource "aws_vpc" "this" {
  count                = local.create_vpc ? 1 : 0
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = var.name }
}

resource "aws_internet_gateway" "this" {
  count  = local.create_vpc ? 1 : 0
  vpc_id = aws_vpc.this[0].id
  tags   = { Name = var.name }
}

resource "aws_subnet" "public" {
  count             = local.create_vpc ? var.az_count : 0
  vpc_id            = aws_vpc.this[0].id
  availability_zone = data.aws_availability_zones.available[0].names[count.index]
  cidr_block        = cidrsubnet(var.vpc_cidr, 8, count.index)
  tags              = { Name = "${var.name}-public-${data.aws_availability_zones.available[0].names[count.index]}" }
}

resource "aws_route_table" "public" {
  count  = local.create_vpc ? 1 : 0
  vpc_id = aws_vpc.this[0].id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this[0].id
  }
  tags = { Name = "${var.name}-public" }
}

resource "aws_route_table_association" "public" {
  count          = local.create_vpc ? var.az_count : 0
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public[0].id
}

# 既定のセキュリティグループは何も許可しない状態にしておく
resource "aws_default_security_group" "this" {
  count  = local.create_vpc ? 1 : 0
  vpc_id = aws_vpc.this[0].id
}

locals {
  vpc_id           = local.create_vpc ? aws_vpc.this[0].id : var.vpc_id
  subnet_ids       = local.create_vpc ? aws_subnet.public[*].id : var.subnet_ids
  assign_public_ip = local.create_vpc ? true : var.assign_public_ip
}

check "existing_vpc_needs_subnets" {
  assert {
    condition     = local.create_vpc || length(var.subnet_ids) > 0
    error_message = "vpc_id を指定する場合は subnet_ids も指定してください。"
  }
}
