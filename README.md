# realm-agent

## 一键安装

使用 **root** 执行，支持 Debian（systemd）和 Alpine（OpenRC），支持 amd64、arm64。

```sh
curl -fsSLo /aaa.sh https://raw.githubusercontent.com/yuandatou1010/realm-agent/main/install.sh && chmod 700 /aaa.sh && sh /aaa.sh
```

安装完成后，输入以下任意命令打开菜单：

```sh
aaa
```

```sh
AAA
```

## 功能

- 基于 realm 进行 TCP / UDP 转发，默认 TCP+UDP。
- Agent 启动或重启时，从远程 HTTPS 地址获取 JSON 规则。
- 规则无变化，不重启正在运行的 realm。
- 不定时查询，不开放管理端口，不保存运行日志。
- 获取失败时保留原有转发；新配置应用失败时尝试回滚。
- 下载预编译程序，无需在服务器安装 Go 或编译。

## 管理菜单

```text
========== Realm 转发管理 ==========
安装状态：已安装
运行状态：运行中
配置地址：https://example.com/realm.json

1. Agent
2. 配置
3. 重启
4. 卸载
5. 查看
0. 退出
```

| 选项 | 说明 |
|---|---|
| 1. Agent | 重启 Agent，触发一次规则同步 |
| 2. 配置 | 设置远程 JSON 地址，立即同步 |
| 3. 重启 | 同步规则，有变化才启动或重启 realm |
| 4. 卸载 | 输入 `333` 确认完全卸载本工具 |
| 5. 查看 | 按远程配置顺序显示本地已应用规则 |
| 0. 退出 | 退出菜单，不影响转发 |

## 远程配置示例

将以下 JSON 放在可直接访问的 HTTPS 地址中，再通过菜单 `2` 配置地址：

```json
{
  "rules": [
    {
      "listen_port": 10001,
      "target_port": 443,
      "target_host": "example.com",
      "protocol": "tcp+udp"
    },
    {
      "listen_port": 10002,
      "target_port": 8080,
      "target_host": "1.2.3.4",
      "protocol": "tcp"
    },
    {
      "listen_port": 10003,
      "target_port": 53,
      "target_host": "8.8.8.8",
      "protocol": "udp"
    }
  ]
}
```

- `listen_port`：入站端口。
- `target_port`：目标端口。
- `target_host`：目标 IP 或域名。
- `protocol`：支持 `tcp+udp`、`tcp`、`udp`，省略时默认 `tcp+udp`。
- `rules: []`：停止全部转发。

## 更新

重新执行一键安装命令即可更新程序，保留已有配置。**更新安装会短暂中断转发。**

## 注意事项

- 安装目录：`/etc/realm-agent`。
- 远程地址必须直接返回 JSON，不支持重定向。
- 修改远程 JSON 后，需要通过菜单触发同步，或等待 Agent 下次启动。
- 默认监听所有 IPv4 网卡，请自行放行防火墙和云安全组端口。
- 远程规则未使用数字签名，请保护配置网站，能够修改 JSON 的人就能改变转发目标。
