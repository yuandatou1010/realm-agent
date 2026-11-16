package main

import (
    "bytes"
    "context"
    "crypto/sha256"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "net"
    "net/http"
    "os"
    "os/exec"
    "os/signal"
    "path/filepath"
    "sort"
    "strconv"
    "strings"
    "syscall"
    "time"
)

const (
    base         = "/etc/realm-agent"
    version      = "1.2.0"
    realmService = "realm-agent-realm"

    maxBody  = 1024 * 1024
    maxRules = 1000
)

type Rule struct {
    ListenPort int    `json:"listen_port"`
    TargetPort int    `json:"target_port"`
    TargetHost string `json:"target_host"`
    Protocol   string `json:"protocol"`
}

type Document struct {
    Rules *[]Rule `json:"rules"`
}

type State struct {
    Rules []Rule `json:"rules"`
}

type Agent struct {
    quiet bool
}

type Snapshot struct {
    name   string
    data   []byte
    exists bool
}

func file(name string) string {
    return filepath.Join(base, name)
}

func (a Agent) say(format string, args ...interface{}) {
    if !a.quiet {
        fmt.Printf(format+"\n", args...)
    }
}

func atomicWrite(name string, data []byte) error {
    f, err := os.CreateTemp(base, ".write-*")
    if err != nil {
        return err
    }

    temp := f.Name()
    defer os.Remove(temp)

    if err := f.Chmod(0600); err != nil {
        f.Close()
        return err
    }

    if _, err := f.Write(data); err != nil {
        f.Close()
        return err
    }

    if err := f.Sync(); err != nil {
        f.Close()
        return err
    }

    if err := f.Close(); err != nil {
        return err
    }

    return os.Rename(temp, file(name))
}

func snapshot(name string) (Snapshot, error) {
    s := Snapshot{name: name}

    data, err := os.ReadFile(file(name))

    if os.IsNotExist(err) {
        return s, nil
    }

    if err != nil {
        return s, err
    }

    s.data = data
    s.exists = true

    return s, nil
}

func restore(s Snapshot) error {
    if s.exists {
        return atomicWrite(s.name, s.data)
    }

    err := os.Remove(file(s.name))
    if os.IsNotExist(err) {
        return nil
    }

    return err
}

func acquireLock() (*os.File, error) {
    f, err := os.OpenFile(
        file("sync.lock"),
        os.O_CREATE|os.O_RDWR,
        0600,
    )
    if err != nil {
        return nil, err
    }

    err = syscall.Flock(
        int(f.Fd()),
        syscall.LOCK_EX|syscall.LOCK_NB,
    )
    if err != nil {
        f.Close()
        return nil, errors.New("已有同步正在执行，请稍后重试")
    }

    return f, nil
}

func systemName() string {
    data, _ := os.ReadFile(file("system"))
    return strings.TrimSpace(string(data))
}

func service(action string) error {
    ctx, cancel := context.WithTimeout(
        context.Background(),
        60*time.Second,
    )
    defer cancel()

    var cmd *exec.Cmd

    if systemName() == "debian" {
        if action == "start" || action == "restart" {
            resetCtx, resetCancel := context.WithTimeout(
                context.Background(),
                10*time.Second,
            )

            _ = exec.CommandContext(
                resetCtx,
                "systemctl",
                "reset-failed",
                realmService+".service",
            ).Run()

            resetCancel()
        }

        cmd = exec.CommandContext(
            ctx,
            "systemctl",
            action,
            realmService+".service",
        )
    } else {
        cmd = exec.CommandContext(
            ctx,
            "rc-service",
            realmService,
            action,
        )
    }

    // 默认不连接标准输出、标准错误，不保存日志。
    if err := cmd.Run(); err != nil {
        return fmt.Errorf("realm %s 失败：%w", action, err)
    }

    return nil
}

func running() bool {
    ctx, cancel := context.WithTimeout(
        context.Background(),
        10*time.Second,
    )
    defer cancel()

    if systemName() == "debian" {
        return exec.CommandContext(
            ctx,
            "systemctl",
            "is-active",
            "--quiet",
            realmService+".service",
        ).Run() == nil
    }

    return exec.CommandContext(
        ctx,
        "rc-service",
        realmService,
        "status",
    ).Run() == nil
}

func validHost(host string) bool {
    if host == "" ||
        len(host) > 253 ||
        strings.TrimSpace(host) != host {
        return false
    }

    if net.ParseIP(host) != nil {
        return true
    }

    host = strings.TrimSuffix(host, ".")

    if host == "" {
        return false
    }

    for _, label := range strings.Split(host, ".") {
        if len(label) == 0 || len(label) > 63 {
            return false
        }

        if label[0] == '-' || label[len(label)-1] == '-' {
            return false
        }

        for _, c := range label {
            if !((c >= 'a' && c <= 'z') ||
                (c >= 'A' && c <= 'Z') ||
                (c >= '0' && c <= '9') ||
                c == '-') {
                return false
            }
        }
    }

    return true
}

func validate(rules []Rule) error {
    if len(rules) > maxRules {
        return fmt.Errorf("规则不能超过 %d 条", maxRules)
    }

    occupied := make(map[string]bool)

    for i := range rules {
        r := &rules[i]

        r.Protocol = strings.ToLower(
            strings.TrimSpace(r.Protocol),
        )

        if r.Protocol == "" {
            r.Protocol = "tcp+udp"
        }

        if r.ListenPort < 1 || r.ListenPort > 65535 {
            return fmt.Errorf("第 %d 条入站端口无效", i+1)
        }

        if r.TargetPort < 1 || r.TargetPort > 65535 {
            return fmt.Errorf("第 %d 条目标端口无效", i+1)
        }

        if !validHost(r.TargetHost) {
            return fmt.Errorf("第 %d 条目标地址无效", i+1)
        }

        if r.Protocol != "tcp" &&
            r.Protocol != "udp" &&
            r.Protocol != "tcp+udp" {
            return fmt.Errorf("第 %d 条协议无效", i+1)
        }

        protocols := []string{r.Protocol}

        if r.Protocol == "tcp+udp" {
            protocols = []string{"tcp", "udp"}
        }

        for _, protocol := range protocols {
            key := fmt.Sprintf(
                "%s:%d",
                protocol,
                r.ListenPort,
            )

            if occupied[key] {
                return fmt.Errorf("监听端口冲突：%s", key)
            }

            occupied[key] = true
        }
    }

    return nil
}

func fetch(address string) ([]Rule, error) {
    req, err := http.NewRequest(
        http.MethodGet,
        address,
        nil,
    )
    if err != nil {
        return nil, errors.New("远程地址无效")
    }

    u := req.URL

    if u.Scheme != "https" ||
        u.Hostname() == "" ||
        u.User != nil ||
        u.Fragment != "" ||
        u.Opaque != "" {
        return nil, errors.New(
            "必须使用不含账号密码和片段的 HTTPS 地址",
        )
    }

    client := &http.Client{
        Timeout: 25 * time.Second,
        CheckRedirect: func(
            req *http.Request,
            via []*http.Request,
        ) error {
            return errors.New("不允许重定向")
        },
    }

    req.Header.Set("Accept", "application/json")
    req.Header.Set("Cache-Control", "no-cache")
    req.Header.Set("User-Agent", "realm-agent/"+version)

    resp, err := client.Do(req)
    if err != nil {
        return nil, errors.New(
            "获取失败，请检查网络、证书及地址是否重定向",
        )
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf(
            "远程返回 HTTP %d",
            resp.StatusCode,
        )
    }

    body, err := io.ReadAll(
        io.LimitReader(resp.Body, maxBody+1),
    )
    if err != nil {
        return nil, errors.New("读取远程配置失败")
    }

    if len(body) > maxBody {
        return nil, errors.New("配置超过 1 MiB")
    }

    var doc Document

    decoder := json.NewDecoder(bytes.NewReader(body))
    decoder.DisallowUnknownFields()

    if err := decoder.Decode(&doc); err != nil {
        return nil, fmt.Errorf("JSON 格式错误：%w", err)
    }

    var extra interface{}

    if err := decoder.Decode(&extra); err != io.EOF {
        return nil, errors.New("JSON 后存在额外内容")
    }

    if doc.Rules == nil {
        return nil, errors.New(
            "必须提供 rules 数组，不能缺失或为 null",
        )
    }

    rules := *doc.Rules

    if err := validate(rules); err != nil {
        return nil, err
    }

    return rules, nil
}

func sortedRules(rules []Rule) []Rule {
    result := append([]Rule{}, rules...)

    sort.Slice(result, func(i, j int) bool {
        a, _ := json.Marshal(result[i])
        b, _ := json.Marshal(result[j])
        return string(a) < string(b)
    })

    return result
}

func fingerprint(rules []Rule) [32]byte {
    data, _ := json.Marshal(sortedRules(rules))
    return sha256.Sum256(data)
}

func realmConfig(rules []Rule) []byte {
    var b strings.Builder

    b.WriteString("# Managed by realm-agent\n")

    // 固定生成顺序，展示顺序单独保存在 state.json。
    for _, r := range sortedRules(rules) {
        listen := net.JoinHostPort(
            "0.0.0.0",
            strconv.Itoa(r.ListenPort),
        )

        remote := net.JoinHostPort(
            r.TargetHost,
            strconv.Itoa(r.TargetPort),
        )

        fmt.Fprintf(
            &b,
            "\n[[endpoints]]\n"+
                "listen = %s\n"+
                "remote = %s\n"+
                "[endpoints.network]\n"+
                "no_tcp = %t\n"+
                "use_udp = %t\n",
            strconv.Quote(listen),
            strconv.Quote(remote),
            r.Protocol == "udp",
            r.Protocol != "tcp",
        )
    }

    return []byte(b.String())
}

func loadState() (State, error) {
    var state State

    data, err := os.ReadFile(file("state.json"))
    if err != nil {
        return state, err
    }

    if err := json.Unmarshal(data, &state); err != nil {
        return state, err
    }

    if state.Rules == nil {
        return state, errors.New("本地规则无效")
    }

    if err := validate(state.Rules); err != nil {
        return state, err
    }

    return state, nil
}

func saveState(rules []Rule) error {
    if rules == nil {
        rules = []Rule{}
    }

    data, err := json.MarshalIndent(
        State{Rules: rules},
        "",
        "  ",
    )
    if err != nil {
        return err
    }

    return atomicWrite("state.json", data)
}

func waitRunning() error {
    time.Sleep(3 * time.Second)

    if !running() {
        return errors.New("realm 启动后退出")
    }

    return nil
}

func ensureRunning(rules []Rule) error {
    if len(rules) == 0 {
        if running() {
            return service("stop")
        }
        return nil
    }

    if running() {
        return nil
    }

    if _, err := os.Stat(file("realm.toml")); err != nil {
        return errors.New("本地 realm 配置不存在")
    }

    if err := service("start"); err != nil {
        return err
    }

    return waitRunning()
}

func (a Agent) sync(newAddress string) error {
    lockFile, err := acquireLock()
    if err != nil {
        return err
    }
    defer lockFile.Close()

    address := newAddress

    if address == "" {
        data, err := os.ReadFile(file("url"))
        if err != nil {
            return errors.New("尚未配置地址，请选择菜单 2")
        }

        address = strings.TrimSpace(string(data))
    }

    oldState, stateErr := loadState()
    haveOld := stateErr == nil

    rules, err := fetch(address)
    if err != nil {
        // 新地址失败不改变现有转发。
        // 普通启动或同步失败时，尝试恢复历史转发。
        if newAddress == "" && haveOld {
            if fallbackErr := ensureRunning(
                oldState.Rules,
            ); fallbackErr != nil {
                return fmt.Errorf(
                    "%v；恢复历史配置失败：%w",
                    err,
                    fallbackErr,
                )
            }

            a.say("获取失败，保留历史转发。")
        }

        return err
    }

    configSnapshot, err := snapshot("realm.toml")
    if err != nil {
        return err
    }

    stateSnapshot, err := snapshot("state.json")
    if err != nil {
        return err
    }

    urlSnapshot, err := snapshot("url")
    if err != nil {
        return err
    }

    newConfig := realmConfig(rules)

    unchanged := haveOld &&
        configSnapshot.exists &&
        fingerprint(rules) == fingerprint(oldState.Rules) &&
        bytes.Equal(
            configSnapshot.data,
            realmConfig(oldState.Rules),
        )

    wasRunning := running()
    serviceTouched := false

    // 应用失败或保存状态失败时，恢复原配置及元数据。
    rollback := func(cause error) error {
        var problems []string

        if serviceTouched {
            if err := service("stop"); err != nil {
                problems = append(
                    problems,
                    "停止服务失败",
                )
            }
        }

        for _, s := range []Snapshot{
            configSnapshot,
            stateSnapshot,
            urlSnapshot,
        } {
            if err := restore(s); err != nil {
                problems = append(
                    problems,
                    "恢复 "+s.name+" 失败",
                )
            }
        }

        if serviceTouched &&
            wasRunning &&
            configSnapshot.exists {

            if err := service("start"); err != nil {
                problems = append(
                    problems,
                    "恢复旧服务失败",
                )
            } else if err := waitRunning(); err != nil {
                problems = append(
                    problems,
                    "旧服务未恢复运行",
                )
            }
        }

        if len(problems) > 0 {
            return fmt.Errorf(
                "%v；回滚异常：%s",
                cause,
                strings.Join(problems, "；"),
            )
        }

        return fmt.Errorf("操作失败，已恢复原配置：%w", cause)
    }

    if !unchanged {
        if configSnapshot.exists {
            if err := atomicWrite(
                "realm.toml.bak",
                configSnapshot.data,
            ); err != nil {
                return fmt.Errorf("备份配置失败：%w", err)
            }
        }

        if err := atomicWrite(
            "realm.toml",
            newConfig,
        ); err != nil {
            return err
        }
    }

    if len(rules) == 0 {
        if wasRunning {
            serviceTouched = true

            if err := service("stop"); err != nil {
                return rollback(err)
            }
        }
    } else if !unchanged || !wasRunning {
        serviceTouched = true

        action := "start"
        if wasRunning {
            action = "restart"
        }

        if err := service(action); err != nil {
            return rollback(err)
        }

        if err := waitRunning(); err != nil {
            return rollback(err)
        }
    }

    // 保存 JSON 的展示顺序，不保存日志或同步历史。
    if err := saveState(rules); err != nil {
        return rollback(
            fmt.Errorf("保存规则失败：%w", err),
        )
    }

    if newAddress != "" {
        if err := atomicWrite(
            "url",
            []byte(newAddress+"\n"),
        ); err != nil {
            return rollback(
                fmt.Errorf("保存地址失败：%w", err),
            )
        }
    }

    if unchanged && wasRunning && len(rules) > 0 {
        a.say("规则无变化，未重启 realm。")
    } else if len(rules) == 0 {
        a.say("规则为空，已停止全部转发。")
    }

    a.say("同步完成，共 %d 条规则。", len(rules))
    return nil
}

func show() error {
    if running() {
        fmt.Println("运行状态：运行中")
    } else {
        fmt.Println("运行状态：未运行")
    }

    state, err := loadState()
    if err != nil || len(state.Rules) == 0 {
        fmt.Println("暂无已应用转发规则。")
        return nil
    }

    fmt.Println()
    fmt.Println("协议       入站端口  目标端口  目标地址")

    // 保留远程 JSON 中的原始顺序。
    for _, r := range state.Rules {
        fmt.Printf(
            "%-10s %-9d %-9d %s\n",
            strings.ToUpper(r.Protocol),
            r.ListenPort,
            r.TargetPort,
            r.TargetHost,
        )
    }

    return nil
}

func main() {
    if len(os.Args) < 2 {
        fmt.Fprintln(
            os.Stderr,
            "用法：agent run|sync|configure URL|show|version",
        )
        os.Exit(1)
    }

    command := os.Args[1]

    if command == "version" {
        fmt.Println(version)
        return
    }

    if os.Geteuid() != 0 {
        fmt.Fprintln(os.Stderr, "请使用 root 运行。")
        os.Exit(1)
    }

    if command == "run" {
        signals := make(chan os.Signal, 1)

        signal.Notify(
            signals,
            syscall.SIGTERM,
            syscall.SIGINT,
        )
        defer signal.Stop(signals)

        agent := Agent{quiet: true}

        // 仅启动时同步一次。
        // 不输出日志，不执行定时查询。
        _ = agent.sync("")

        <-signals

        // 停止 Agent 不停止 realm。
        return
    }

    agent := Agent{}
    var err error

    switch command {
    case "sync":
        err = agent.sync("")

    case "configure":
        if len(os.Args) != 3 {
            err = errors.New(
                "用法：agent configure HTTPS_URL",
            )
        } else {
            err = agent.sync(
                strings.TrimSpace(os.Args[2]),
            )
        }

    case "show":
        err = show()

    default:
        err = errors.New("未知命令")
    }

    if err != nil {
        fmt.Fprintln(
            os.Stderr,
            "操作失败："+err.Error(),
        )
        os.Exit(1)
    }
}
