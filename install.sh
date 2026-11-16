#!/bin/sh
set -eu

APP="/etc/realm-agent"

REPO="${AAA_REPO:-yuandatou1010/realm-agent}"

AGENT_SERVICE="realm-agent"
REALM_SERVICE="realm-agent-realm"

die() {
    echo "错误：$*" >&2
    exit 1
}

[ "$(id -u)" = "0" ] || die "请使用 root 运行。"

[ -f /etc/os-release ] || die "无法识别系统。"

. /etc/os-release

case "$ID" in
    debian)
        command -v systemctl >/dev/null 2>&1 ||
            die "Debian 需要 systemd。"

        [ -d /run/systemd/system ] ||
            die "当前系统未运行 systemd。"

        SYSTEM="debian"
        ;;

    alpine)
        command -v rc-service >/dev/null 2>&1 ||
            die "Alpine 需要 OpenRC。"

        command -v supervise-daemon >/dev/null 2>&1 ||
            die "当前 OpenRC 缺少 supervise-daemon。"

        SYSTEM="alpine"
        ;;

    *)
        die "仅支持 Debian 和 Alpine，当前系统：$ID"
        ;;
esac

case "$(uname -m)" in
    x86_64)
        REALM_ARCH="x86_64"
        AGENT_ARCH="amd64"
        ;;

    aarch64|arm64)
        REALM_ARCH="aarch64"
        AGENT_ARCH="arm64"
        ;;

    *)
        die "暂不支持此架构：$(uname -m)"
        ;;
esac

# 不覆盖其他程序或旧版程序的快捷入口。
for name in AAA aaa; do
    shortcut="/usr/local/bin/$name"

    if [ -e "$shortcut" ] || [ -L "$shortcut" ]; then
        if [ ! -L "$shortcut" ] ||
           [ "$(readlink "$shortcut")" != "$APP/aaa" ]; then
            die "$shortcut 已被占用。如果是旧版，请先卸载旧版。"
        fi
    fi
done

echo "检测系统：$SYSTEM"
echo "检测架构：$AGENT_ARCH"
echo "安装必要组件……"

if [ "$SYSTEM" = "debian" ]; then
    apt-get update

    DEBIAN_FRONTEND=noninteractive apt-get install -y \
        --no-install-recommends \
        ca-certificates curl jq tar
else
    apk add --no-cache \
        ca-certificates curl jq tar
fi

umask 077

mkdir -p "$APP" /usr/local/bin
chmod 700 "$APP"

# 工作目录放在安装目录内，不使用 /tmp。
WORK="$(mktemp -d "$APP/.install.XXXXXX")"

cleanup() {
    rm -rf "$WORK"
}

trap cleanup EXIT
trap 'exit 1' HUP INT TERM

fetch() {
    curl --fail --silent --show-error \
        --proto '=https' \
        --tlsv1.2 \
        --connect-timeout 15 \
        --max-time 180 \
        "$1" -o "$2"
}

release_fetch() {
    curl --fail --silent --show-error --location \
        --proto '=https' \
        --proto-redir '=https' \
        --tlsv1.2 \
        --connect-timeout 15 \
        --max-time 300 \
        "$1" -o "$2"
}

asset_url() {
    jq -r --arg name "$1" \
        '.assets[] | select(.name == $name) | .browser_download_url' \
        "$WORK/agent-release.json"
}

verify_asset() {
    name="$1"

    expected="$(
        awk -v name="$name" \
            '$2 == name { print $1 }' \
            "$WORK/SHA256SUMS"
    )"

    [ "${#expected}" -eq 64 ] ||
        die "$name 校验和缺失或格式错误。"

    case "$expected" in
        *[!0-9a-f]*)
            die "$name 校验和格式错误。"
            ;;
    esac

    actual="$(
        sha256sum "$WORK/$name" |
            awk '{print $1}'
    )"

    [ "$expected" = "$actual" ] ||
        die "$name 校验失败。"
}

echo "查询 Agent 发布版本……"

# 可通过 AAA_AGENT_VERSION=v1.2.0 固定版本。
if [ -n "${AAA_AGENT_VERSION:-}" ]; then
    AGENT_API="https://api.github.com/repos/${REPO}/releases/tags/${AAA_AGENT_VERSION}"
else
    AGENT_API="https://api.github.com/repos/${REPO}/releases/latest"
fi

fetch "$AGENT_API" "$WORK/agent-release.json"

AGENT_TAG="$(
    jq -r '.tag_name // empty' "$WORK/agent-release.json"
)"

[ -n "$AGENT_TAG" ] ||
    die "没有找到 Agent Release，请先通过 Actions 发布版本。"

CHECKSUM_URL="$(asset_url SHA256SUMS)"

[ -n "$CHECKSUM_URL" ] ||
    die "Release $AGENT_TAG 缺少 SHA256SUMS。"

release_fetch "$CHECKSUM_URL" "$WORK/SHA256SUMS"

AGENT_ASSET="aaa-agent-linux-${AGENT_ARCH}"

# Agent、菜单、安装脚本全部取自同一 Release。
for name in "$AGENT_ASSET" menu.sh install.sh; do
    url="$(asset_url "$name")"

    [ -n "$url" ] ||
        die "Release $AGENT_TAG 缺少 $name。"

    echo "下载：$name"

    release_fetch "$url" "$WORK/$name"
    verify_asset "$name"
done

mv "$WORK/$AGENT_ASSET" "$WORK/agent"
mv "$WORK/menu.sh" "$WORK/aaa"

sh -n "$WORK/aaa"
sh -n "$WORK/install.sh"

echo "查询 realm 发布版本……"

# 可通过 REALM_VERSION=vX.Y.Z 固定 realm 版本。
if [ -n "${REALM_VERSION:-}" ]; then
    REALM_API="https://api.github.com/repos/zhboner/realm/releases/tags/${REALM_VERSION}"
else
    REALM_API="https://api.github.com/repos/zhboner/realm/releases/latest"
fi

fetch "$REALM_API" "$WORK/realm-release.json"

REALM_ASSET="realm-${REALM_ARCH}-unknown-linux-musl.tar.gz"

REALM_URL="$(
    jq -r --arg name "$REALM_ASSET" \
        '.assets[] | select(.name == $name) | .browser_download_url' \
        "$WORK/realm-release.json"
)"

REALM_TAG="$(
    jq -r '.tag_name // empty' "$WORK/realm-release.json"
)"

[ -n "$REALM_URL" ] ||
    die "未找到 realm 发行文件：$REALM_ASSET"

echo "下载 realm：$REALM_TAG"

release_fetch "$REALM_URL" "$WORK/realm.tar.gz"

# GitHub API 提供 SHA256 digest 时进行校验。
REALM_DIGEST="$(
    jq -r --arg name "$REALM_ASSET" \
        '.assets[] | select(.name == $name) | (.digest // empty)' \
        "$WORK/realm-release.json"
)"

case "$REALM_DIGEST" in
    sha256:*)
        expected="${REALM_DIGEST#sha256:}"

        actual="$(
            sha256sum "$WORK/realm.tar.gz" |
                awk '{print $1}'
        )"

        [ "$expected" = "$actual" ] ||
            die "realm 下载校验失败。"
        ;;
esac

# 只读取二进制，不解压其他路径。
MEMBER="$(
    tar -tzf "$WORK/realm.tar.gz" |
        awk '$0 == "realm" || $0 == "./realm" { print; exit }'
)"

[ -n "$MEMBER" ] ||
    die "realm 压缩包结构不符合预期。"

tar -xOzf "$WORK/realm.tar.gz" "$MEMBER" > "$WORK/realm"

chmod 755 "$WORK/agent" "$WORK/realm" "$WORK/aaa"
chmod 700 "$WORK/install.sh"

# 下载准备成功后，先验证新程序能否执行。
"$WORK/agent" version >/dev/null 2>&1 ||
    die "Agent 无法运行。"

"$WORK/realm" --version >/dev/null 2>&1 ||
    die "realm 无法运行。"

echo "安装程序和服务……"

if [ "$SYSTEM" = "debian" ]; then
    systemctl stop "$AGENT_SERVICE.service" \
        >/dev/null 2>&1 || true

    systemctl stop "$REALM_SERVICE.service" \
        >/dev/null 2>&1 || true
else
    rc-service "$AGENT_SERVICE" stop \
        >/dev/null 2>&1 || true

    rc-service "$REALM_SERVICE" stop \
        >/dev/null 2>&1 || true
fi

# 保留现有 url、state.json、realm.toml。
mv "$WORK/agent" "$APP/agent"
mv "$WORK/realm" "$APP/realm"
mv "$WORK/aaa" "$APP/aaa"
mv "$WORK/install.sh" "$APP/install.sh"

printf '%s\n' "$SYSTEM" > "$APP/system"
printf '%s\n' "$REPO" > "$APP/repo"
printf '%s\n' "$AGENT_TAG" > "$APP/agent-release"
printf '%s\n' "$REALM_TAG" > "$APP/realm-release"

if [ "$SYSTEM" = "debian" ]; then
    cat > /etc/systemd/system/realm-agent-realm.service <<'EOF'
[Unit]
Description=Realm Agent forwarding service
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=60
StartLimitBurst=5

[Service]
Type=simple
ExecStart=/etc/realm-agent/realm -c /etc/realm-agent/realm.toml
Restart=on-failure
RestartSec=5
UMask=0077
StandardOutput=null
StandardError=null
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF

    cat > /etc/systemd/system/realm-agent.service <<'EOF'
[Unit]
Description=Realm configuration agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/etc/realm-agent/agent run
Restart=on-failure
RestartSec=10
TimeoutStopSec=240
UMask=0077
StandardOutput=null
StandardError=null

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload

    # realm 不独立开机启动，由 Agent 同步后控制。
    systemctl disable "$REALM_SERVICE.service" \
        >/dev/null 2>&1 || true

    systemctl enable "$AGENT_SERVICE.service" \
        >/dev/null 2>&1

    systemctl reset-failed \
        "$AGENT_SERVICE.service" "$REALM_SERVICE.service" \
        >/dev/null 2>&1 || true
else
    cat > /etc/init.d/realm-agent-realm <<'EOF'
#!/sbin/openrc-run

name="Realm forwarding"
description="Realm Agent forwarding service"

supervisor="supervise-daemon"

command="/etc/realm-agent/realm"
command_args="-c /etc/realm-agent/realm.toml"

respawn_delay=5
respawn_max=5
respawn_period=60

output_log="/dev/null"
error_log="/dev/null"

depend() {
    need net
}
EOF

    cat > /etc/init.d/realm-agent <<'EOF'
#!/sbin/openrc-run

name="Realm Agent"
description="Realm configuration agent"

supervisor="supervise-daemon"

command="/etc/realm-agent/agent"
command_args="run"

respawn_delay=10
respawn_max=5
respawn_period=60

retry="TERM/240/KILL/5"

output_log="/dev/null"
error_log="/dev/null"

depend() {
    need net
}
EOF

    chmod 755 \
        /etc/init.d/realm-agent \
        /etc/init.d/realm-agent-realm

    rc-update del "$REALM_SERVICE" default \
        >/dev/null 2>&1 || true

    rc-update add "$AGENT_SERVICE" default \
        >/dev/null 2>&1
fi

ln -sf "$APP/aaa" /usr/local/bin/AAA
ln -sf "$APP/aaa" /usr/local/bin/aaa

if [ "$SYSTEM" = "debian" ]; then
    systemctl start "$AGENT_SERVICE.service"
else
    rc-service "$AGENT_SERVICE" start
fi

echo
echo "安装完成。"
echo "Agent 发布版本：$AGENT_TAG"
echo "realm 发布版本：$REALM_TAG"
echo "安装目录：$APP"
echo "菜单命令：AAA 或 aaa"
echo "首次使用请选择 2 配置远程 JSON 地址。"
