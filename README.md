# hashi

一時的なコンテナを AWS 上に起動して中に入り、そこを踏み台にして他の環境へ SSH するためのコマンドです。使い終わったらコンテナは自動で止まります。

```bash
hashi -e dev ssh ec2-user@10.0.1.23   # 起動 → ssh → ssh を抜けたら停止
hashi -e dev start                    # 起動 → シェルに入る → exit で停止
```

常設の踏み台サーバーを持たずに済み、受信用のポートも開けません。

## 仕組み

使う AWS のサービスと用語を先に説明します。

- **ECS**（Elastic Container Service）: AWS でコンテナを動かすサービス。動いているコンテナ（1 回の起動分）を **タスク** と呼びます。
- **Fargate**: サーバーを用意せずにタスクを動かす ECS の動かし方。動いた時間の分だけ課金されます。
- **ECS Exec**: 動いているタスクの中でコマンドを実行・シェルに入る ECS の機能。通信は SSM Session Manager（AWS の遠隔操作の仕組み）を通り、コンテナから AWS へ向かう外向きの 443 番ポートだけで成り立ちます。
- **session-manager-plugin**: ECS Exec の端末のやり取りを手元で受け持つ AWS 製のプログラム。`aws ecs execute-command` も内部でこれを呼んでいます。

hashi は次の順に動きます。

1. Fargate のタスクを 1 つ起動する。コンテナのコマンドは `sleep <TTL>` にする（TTL = タスクの最長寿命。既定 1 時間）。
2. タスクが動き、ECS Exec の準備ができるまで待つ。
3. ECS Exec でシェル（または ssh など指定したコマンド）を開き、session-manager-plugin で手元の端末とつなぐ。
4. 抜けたらタスクを止める。

手元の PC が落ちたりネットワークが切れたりしてタスクを止められなかった場合でも、TTL が来ればコンテナのコマンド（sleep）が終わり、タスクは自分で止まります。

## 構成

| パス | 内容 |
|---|---|
| `cmd/hashi/` | コマンド本体（Go）。Linux / Mac / Windows 向けの単一の実行ファイルになる |
| `image/` | 踏み台コンテナのイメージ（Debian に ssh・sshpass・cloudflared・nc・dig などを入れたもの） |
| `terraform/` | AWS 側の資源（ECR リポジトリ・ECS クラスター・タスク定義・IAM ロール・セキュリティグループ・ロググループ） |

## 導入（管理者が環境ごとに 1 回）

### 1. AWS 側の資源を作る

```bash
cd terraform
cp terraform.tfvars.example terraform.tfvars   # リージョンなどを書く
terraform init
terraform apply
```

既定では、hashi 専用の VPC（パブリックサブネット 2 つとインターネットゲートウェイだけの最小構成）を新しく作ります。タスクはパブリック IP で直接外へ出ます。NAT ゲートウェイや VPC エンドポイントは作らないので、常設の費用はかかりません。

既存の VPC の中のサーバーに入りたい場合だけ、`vpc_id` と `subnet_ids` を指定して既存の VPC に置きます。そのサブネットは次を満たす必要があります。

- SSH の接続先に届くこと。
- 443 番ポートで AWS の API（SSM・ECR・CloudWatch Logs）へ出られること。NAT ゲートウェイがあるか、次の VPC エンドポイントがあること: `ssmmessages`、`ecr.api`、`ecr.dkr`、`s3`（ゲートウェイ型）、`logs`。

作られるセキュリティグループは、受信をすべて拒否し、外向きは 443 番（AWS の API 用）と、`ssh_target_cidrs` で指定した宛先の SSH ポートだけを許可します。DB への疎通確認などが必要なら `extra_egress_rules` で足します。

### 2. イメージを ECR に置く

ECR（Elastic Container Registry）は AWS のコンテナイメージ置き場です。docker が使える端末で次を実行します。

```bash
make image ECR=$(terraform -chdir=terraform output -raw ecr_repository_url)
```

Terraform で `cpu_architecture = "ARM64"` にした場合は `PLATFORM=linux/arm64` も付けます。

### 3. 使う人に権限を付ける

`terraform output user_policy_arn` のポリシーを、使う人の IAM Identity Center の許可セット・IAM ユーザー・グループに付けます。中身は、このクラスターでのタスクの起動・一覧・停止・ECS Exec と、2 つのロールの受け渡し（`iam:PassRole`）だけです。

## 使う人の準備

1. [session-manager-plugin](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html) をインストールする。
2. hashi をビルドして PATH の通った場所に置く（`make build` で `bin/hashi`、`make dist` で各 OS 向け）。
3. 設定ファイルを書く（次の節）。

### 設定ファイル

場所は `~/.config/hashi/config.yaml`（Windows は `%AppData%\hashi\config.yaml`）。環境変数 `HASHI_CONFIG` か `-c` で変えられます。

`envs` の下に接続先（環境）ごとの設定を書きます。中身は管理者が `terraform output -raw hashi_env_config` で出したものを貼り、`profile`（AWS のプロファイル名）を足します。

```yaml
default_env: dev
envs:
  dev:
    profile: dev-admin
    region: ap-northeast-1
    cluster: hashi
    task_definition: hashi
    container: hashi
    subnets: [subnet-aaaaaaaa, subnet-bbbbbbbb]
    security_groups: [sg-xxxxxxxx]
    assign_public_ip: false
    ttl: 1h
  # fargate_spot: true にすると安くなるが、AWS の都合で途中で止められることがある
  # shell: /bin/bash -l    入ったときのシェル
```

接続先は `-e` > 環境変数 `HASHI_ENV` > `default_env` > （1 つしかなければ）それ、の順で決まります。

## 使い方

```bash
hashi -e dev start                       # シェルに入る。exit で抜けるとタスクを止める
hashi -e dev ssh ec2-user@10.0.1.23      # 起動して ssh する。ssh を抜けるとタスクを止める
hashi -e dev ssh -- -p 2222 u@10.0.1.23  # - で始まる引数は -- の後ろに書く
hashi -e dev exec 'nc -zv 10.0.1.23 5432'  # 任意のコマンド（シェルで解釈される）

hashi -e dev start --ttl 3h              # 長く使うとき（上限 12h）
hashi -e dev start --keep                # 抜けても止めない
hashi -e dev ls                          # 自分のタスクの一覧（--all で全員分）
hashi -e dev attach <タスクID>           # 動いているタスクに入り直す
hashi -e dev stop <タスクID>             # 止める（--mine で自分のものをすべて）
hashi envs                               # 設定の接続先の一覧
```

SSH はパスワード認証で使う想定です。`hashi ssh` の中でパスワードを聞かれたら入力します。

起動から入れるようになるまで、イメージの取得などで 30 秒〜1 分ほどかかります。

## 注意

- **操作は記録されます。** ECS Exec のセッションの入出力は CloudWatch Logs の `/hashi/<name>/exec` に残ります（監査用）。パスワードのプロンプトへの入力は画面に出ないので残りませんが、`sshpass -p <パスワード>` のようにコマンドに書いたものは残ります。
- **接続先のホスト鍵は毎回確認を求められます。** コンテナは毎回新しく作られ、`known_hosts`（接続したことのあるホストの鍵の記録）が残らないためです。
- **コンテナの中は使い捨てです。** 止めるとファイルは消えます。
- **終了コードは返りません。** ECS Exec の仕組み上、`hashi exec` はコンテナ内のコマンドの終了コードを返せません。
- **ポート転送には対応していません。** 手元の DB クライアントから接続先へつなぐような使い方は、別途 SSM のポート転送などを検討してください。
