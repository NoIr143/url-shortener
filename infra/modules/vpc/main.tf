# Three-AZ VPC (DEC-003: single-region, three-AZ topology) with one public
# and one private subnet per AZ. ECS/Fargate tasks and the internal ALB
# live in the private subnets; the public ALB and the NAT gateway live in
# the public subnets.
#
# /16 VPC split into /24s (newbits=8): index 0-2 -> public subnets,
# index 10-12 -> private subnets. The gap leaves room for a future
# isolated/data subnet tier without renumbering anything already in use.

resource "aws_vpc" "this" {
  cidr_block           = var.cidr_block
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = merge(var.tags, { Name = var.name })
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = merge(var.tags, { Name = "${var.name}-igw" })
}

resource "aws_subnet" "public" {
  for_each = { for idx, az in var.azs : az => idx }

  vpc_id                  = aws_vpc.this.id
  availability_zone       = each.key
  cidr_block              = cidrsubnet(var.cidr_block, 8, each.value)
  map_public_ip_on_launch = true

  tags = merge(var.tags, { Name = "${var.name}-public-${each.key}", Tier = "public" })
}

resource "aws_subnet" "private" {
  for_each = { for idx, az in var.azs : az => idx }

  vpc_id            = aws_vpc.this.id
  availability_zone = each.key
  cidr_block        = cidrsubnet(var.cidr_block, 8, each.value + 10)

  tags = merge(var.tags, { Name = "${var.name}-private-${each.key}", Tier = "private" })
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id
  tags   = merge(var.tags, { Name = "${var.name}-public" })

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this.id
  }
}

resource "aws_route_table_association" "public" {
  for_each = aws_subnet.public

  subnet_id      = each.value.id
  route_table_id = aws_route_table.public.id
}

# Single NAT gateway, not one per AZ: a deliberate cost/availability
# tradeoff for a self-funded MVP (docs/decisions/DEC-001.md,
# docs/COST_MODEL.md) — one NAT is roughly a third of the cost of one per
# AZ. It only affects private-subnet egress (e.g. pulling images from
# ECR); ALB routing and DynamoDB read/write remain multi-AZ regardless,
# so this does not weaken the AZ-outage RTO/RPO target in
# docs/decisions/DEC-003.md. The residual risk is real and specific: if
# the NAT's own AZ has an outage, private-subnet tasks in the *other*
# surviving AZs also lose internet egress until it's replaced — revisit
# if/when real traffic or budget justifies one NAT per AZ.
resource "aws_eip" "nat" {
  domain = "vpc"
  tags   = merge(var.tags, { Name = "${var.name}-nat" })
}

resource "aws_nat_gateway" "this" {
  allocation_id = aws_eip.nat.id
  subnet_id     = aws_subnet.public[var.azs[0]].id
  tags          = merge(var.tags, { Name = "${var.name}-nat" })

  depends_on = [aws_internet_gateway.this]
}

resource "aws_route_table" "private" {
  vpc_id = aws_vpc.this.id
  tags   = merge(var.tags, { Name = "${var.name}-private" })

  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.this.id
  }
}

resource "aws_route_table_association" "private" {
  for_each = aws_subnet.private

  subnet_id      = each.value.id
  route_table_id = aws_route_table.private.id
}
