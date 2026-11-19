# hashi の踏み台コンテナ。ECS Exec で入り、ここから ssh などで他の環境へ接続する。
# ECS Exec のエージェント（SSM エージェント）は Fargate が外から差し込むので、イメージに入れる必要はない。
FROM debian:bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bash ca-certificates openssh-client sshpass \
      curl dnsutils iputils-ping netcat-openbsd telnet traceroute \
      less vim-tiny procps tzdata locales \
 && rm -rf /var/lib/apt/lists/*

# cloudflared（Cloudflare Tunnel / Access 経由の接続用）を Cloudflare 公式の apt リポジトリから入れる
RUN curl -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg -o /usr/share/keyrings/cloudflare-main.gpg \
 && echo 'deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared any main' \
      > /etc/apt/sources.list.d/cloudflared.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends cloudflared \
 && rm -rf /var/lib/apt/lists/* \
 && cloudflared --version

ENV TZ=Asia/Tokyo \
    LANG=C.UTF-8

# 接続が無通信で切れないようにする
COPY ssh_config /etc/ssh/ssh_config.d/hashi.conf
# hashi exec / hashi ssh のコマンドを受け取る補助スクリプト
COPY hashi-run /usr/local/bin/hashi-run
RUN chmod 0755 /usr/local/bin/hashi-run

WORKDIR /root
# 既定のコマンド。hashi は起動時に sleep <TTL> で上書きする
CMD ["sleep", "3600"]
