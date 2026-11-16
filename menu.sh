#!/bin/sh

APP="/etc/realm-agent"

AGENT_SERVICE="realm-agent"
REALM_SERVICE="realm-agent-realm"

[ "$(id -u)" = "0" ] || {
    echo "请使用 root 运行。"
    exit 1
}

[ -x "$APP/agent" ] || {
    echo "Agent 未安装，请重新执行一键安装命令。"
    exit 1
}

system_name() {
    cat "$APP/system" 2>/dev/null
}

realm_running() {
    if [ "$(system_name)" = "debian" ]; then
        systemctl is-active --quiet "$REALM_SERVICE.service"
    else
        rc-service "$REALM_SERVICE" status >/dev/null 2>&1
    fi
}

restart_agent() {
    if [ "$(system_name)" = "debian" ]; then
        systemctl restart "$AGENT_SERVICE.service"
    else
        rc-service "$AGENT_SERVICE" restart
    fi
}

uninstall_all() {
    echo
    echo "将停止转发，并删除本工具的程序、服务及全部配置。"
    printf "输入 333 确认完全卸载，其他输入取消："

    IFS= read -r confirm

    [ "$confirm" = "333" ] || {
        echo "已取消。"
        return
    }

    system="$(system_name)"

    if [ "$system" = "debian" ]; then
        systemctl disable --now "$AGENT_SERVICE.service" \
            >/dev/null 2>&1 || true

        systemctl disable --now "$REALM_SERVICE.service" \
            >/dev/null 2>&1 || true

        rm -f \
            "/etc/systemd/system/$AGENT_SERVICE.service" \
            "/etc/systemd/system/$REALM_SERVICE.service"

        systemctl daemon-reload

        systemctl reset-failed \
            "$AGENT_SERVICE.service" "$REALM_SERVICE.service" \
            >/dev/null 2>&1 || true
    else
        rc-service "$AGENT_SERVICE" stop \
            >/dev/null 2>&1 || true

        rc-service "$REALM_SERVICE" stop \
            >/dev/null 2>&1 || true

        rc-update del "$AGENT_SERVICE" default \
            >/dev/null 2>&1 || true

        rc-update del "$REALM_SERVICE" default \
            >/dev/null 2>&1 || true

        rm -f \
            "/etc/init.d/$AGENT_SERVICE" \
            "/etc/init.d/$REALM_SERVICE"
    fi

    for shortcut in /usr/local/bin/AAA /usr/local/bin/aaa; do
        if [ -L "$shortcut" ] &&
           [ "$(readlink "$shortcut")" = "$APP/aaa" ]; then
            rm -f "$shortcut"
        fi
    done

    # 内容一致时才删除根目录安装脚本。
    if [ -f /aaa.sh ] &&
       cmp -s /aaa.sh "$APP/install.sh"; then
        rm -f /aaa.sh
    fi

    rm -rf "$APP"

    echo "已完全卸载本工具。"
    echo "共享系统组件未删除，避免影响其他程序。"
    exit 0
}

while :; do
    installed="未安装"
    running="未运行"
    address="未配置"

    if [ -x "$APP/agent" ] && [ -x "$APP/realm" ]; then
        installed="已安装"
    fi

    if realm_running; then
        running="运行中"
    fi

    if [ -s "$APP/url" ]; then
        address="$(cat "$APP/url")"
    fi

    echo
    echo "========== Realm 转发管理 =========="
    echo "安装状态：$installed"
    echo "运行状态：$running"
    echo "配置地址：$address"
    echo
    echo "1. Agent"
    echo "2. 配置"
    echo "3. 重启"
    echo "4. 卸载"
    echo "5. 查看"
    echo "0. 退出"
    echo

    printf "请选择："
    IFS= read -r choice || exit 0

    case "$choice" in
        1)
            if restart_agent; then
                echo "Agent 已重启，正在执行一次规则同步。"
                echo "规则无变化时，不重启正在运行的 realm。"
            else
                echo "Agent 重启失败。"
            fi
            ;;

        2)
            printf "请输入远程 JSON HTTPS 地址："
            IFS= read -r address

            if [ -n "$address" ]; then
                "$APP/agent" configure "$address"
            else
                echo "地址不能为空。"
            fi
            ;;

        3)
            "$APP/agent" sync
            ;;

        4)
            uninstall_all
            ;;

        5)
            "$APP/agent" show
            ;;

        0)
            exit 0
            ;;

        *)
            echo "无效选项。"
            ;;
    esac
done
