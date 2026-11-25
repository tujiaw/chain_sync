# chainsync —— 链式目录同步工具（拉取模型）

## 本次四节点部署

网络连接方向：`10.25.109.123 → 10.25.109.120:9030 → 10.26.34.82:9030 → 172.30.202.24:9030`。

文件流向与连接方向相反：`172.30.202.24 → 10.26.34.82 → 10.25.109.120 → 10.25.109.123`。
24 是文件源；82、120 中途放入的文件也会向 123 方向同步，不会回传到 24。123 放入的文件不会传给其他节点。
无需修改服务协议，使用现有拉取模式即可。下面统一假设目录为 `/data/upload/sync`，请替换为实际目录。

### 选择安装包

Kylin V10 与 Ubuntu 20.04 均使用 Linux 静态二进制，无需安装 Go 或 glibc 运行依赖。按 `uname -m` 选择：

| uname -m | 安装包 |
|---|---|
| x86_64 | chainsync-linux-amd64.tar.gz |
| aarch64 | chainsync-linux-arm64.tar.gz |

这两个包不覆盖龙芯、申威等其他架构。当前 Go 工具链要求 Linux 内核至少 3.2（见 [Go 官方 Linux 要求](https://go.dev/wiki/Linux)）；目标机执行 `uname -r` 确认。发行版信息不能代替 CPU/内核信息，也不代表已在目标机器上实测。

Windows PowerShell 构建：`powershell -NoProfile -ExecutionPolicy Bypass -File ./build-linux.ps1`。包与校验和位于 `dist/`。

### nohup 后台长期运行

每台机器解压对应包后进入目录，执行 `chmod +x chainsync`。以下命令分别在对应机器执行，不要全部在同一机器执行。建议按 24、82、120、123 顺序启动。每台先执行 `mkdir -p logs`，日志保存在安装目录的 `logs/` 下，不会进入同步目录。

```bash
# 172.30.202.24（D：文件源）
nohup ./chainsync -name D -listen :9030 -dir /data/upload/sync >> logs/chainsync.log 2>&1 < /dev/null &
echo $! > chainsync.pid

# 10.26.34.82（C：拉取 24，供 120 拉取）
NO_PROXY=172.30.202.24 nohup ./chainsync -name C -listen :9030 -dir /data/upload/sync -upstream http://172.30.202.24:9030 >> logs/chainsync.log 2>&1 < /dev/null &
echo $! > chainsync.pid

# 10.25.109.120（B：拉取 82，供 123 拉取）
NO_PROXY=10.26.34.82 nohup ./chainsync -name B -listen :9030 -dir /data/upload/sync -upstream http://10.26.34.82:9030 >> logs/chainsync.log 2>&1 < /dev/null &
echo $! > chainsync.pid

# 10.25.109.123（A：仅需拉取，监听本机）
NO_PROXY=10.25.109.120 nohup ./chainsync -name A -listen 127.0.0.1:9030 -dir /data/upload/sync -upstream http://10.25.109.120:9030 >> logs/chainsync.log 2>&1 < /dev/null &
echo $! > chainsync.pid
```

所有节点统一使用 `/data/upload/sync`。启动时服务会自动递归创建缺失目录（权限 0755），运行用户需要有创建目录及写入文件的权限。内网 IP 用 NO_PROXY 绕过可能存在的 HTTP 代理。

### 查看、停止与重启

在启动时的安装目录执行：

```bash
# 查看进程及实时日志
ps -p "$(cat chainsync.pid)" -o pid,args
tail -f logs/chainsync.log

# 停止：先确认上面的进程确实是 chainsync，再执行
kill -TERM "$(cat chainsync.pid)"
```

重启时先停止，确认旧进程已经退出，再重新执行本机对应的启动命令。不要重复启动。四台机器分别保留自己的 PID 文件。

`nohup` 允许进程在退出 SSH 后继续运行；进程异常退出或服务器重启后，需要手动重新启动。本部署方式不使用 systemd/service，也不自动重启。日志持续追加，需要定期归档或清理。

### 连通与同步检查

分别在 123、120、82 上执行对应下一跳检查，应返回 HTTP 200：

```bash
# 在 123
curl --noproxy '*' -f http://10.25.109.120:9030/health
# 在 120
curl --noproxy '*' -f http://10.26.34.82:9030/health
# 在 82
curl --noproxy '*' -f http://172.30.202.24:9030/health
```

这些链路需允许 TCP 9030 的请求及响应，无需反向主动建连。服务无认证且使用 HTTP，应限制 9030 仅允许相邻客户端访问。

在 24 的同步目录写入一个唯一命名的测试文件，然后在其他三台用 `sha256sum` 比对。也可在 82 放入另一文件，确认它到达 120、123 且不会到达 24。默认每跳轮询 5 秒，小文件端到端一般需要数轮轮询；大文件还要计入传输及哈希时间。

中间目录会同时保存本机文件和从后方拉取的文件。**不同来源应使用唯一文件名**：若本机文件与上游同名且 size/mtime 不一致，上游版本将覆盖本机版本，不按“较新者优先”判断。请先写隐藏临时文件，再原子重命名为最终文件，避免同步仍在写入的数据。

仅同步顶层非隐藏文件，不含子目录，不传播删除；修改检测依据 size+mtime，二者未变化的内容修改不会被发现。

### 续传与 HTTP 416

拉取中断后使用 `.incoming-文件名.part` 续传。若临时文件已达到或超过清单中的源文件大小，新版会自动从头下载并校验；若源文件在读取清单后发生变化、上游返回 HTTP 416，则重置临时文件，下一轮刷新清单后从头重试。

旧版本若每隔 5 秒持续报 `416 Requested Range Not Satisfiable`，可先停止对应节点，删除该文件的临时副本，再用原启动命令重启。例如 C 节点的 `template_serving.tar`：

```bash
# 先停止 C 节点并确认进程已退出，仅删除此临时副本
rm -f -- /data/upload/sync/.incoming-template_serving.tar.part
```

正式文件 `template_serving.tar` 不需要删除。建议升级为本次修复版，各拉取节点 A/B/C 替换程序前先停止旧进程，再用原 nohup 命令启动。源文件发布时先写隐藏临时文件再原子改名，避免边写边同步。

极简的单向链式目录同步工具：文件从源头 A 依次同步到 B、C、D。

## 模型

```
A  <--拉--  B  <--拉--  C  <--拉--  D
```

- 访问方向固定：**下游访问上游**（B 访问 A，C 访问 B，D 访问 C）。
- 每个节点同时是**服务端**（`/list`、`/file`）和**客户端**（轮询上游）。
- 源头 A 只有服务端，没有上游；叶子 D 只有客户端。

## 构建（交叉编译）

纯 Go 标准库，无第三方依赖，交叉编译零成本。

| 目标 | 命令 |
|------|------|
| Windows amd64 | `GOOS=windows GOARCH=amd64 go build -o chainsync.exe .` |
| Linux amd64   | `GOOS=linux GOARCH=amd64 go build -o chainsync .` |
| Linux arm64   | `GOOS=linux GOARCH=arm64 go build -o chainsync .` |
| Linux armv7   | `GOOS=linux GOARCH=arm GOARM=7 go build -o chainsync .` |

一键编译全部（bash / Git Bash）：

```bash
./build.sh        # 产物输出到 dist/
```

Windows cmd：

```bat
build.bat
```

建议静态编译 `CGO_ENABLED=0`（脚本里已内置），产物可直接丢到目标机器运行。

## 运行

```bash
# A（源头，无 upstream）
$1nohup ./chainsync $2 >> logs/chainsync.log 2>&1 < /dev/null &
echo $! > chainsync.pid

# B（拉 A）
$1nohup ./chainsync $2 >> logs/chainsync.log 2>&1 < /dev/null &
echo $! > chainsync.pid

# C（拉 B）
$1nohup ./chainsync $2 >> logs/chainsync.log 2>&1 < /dev/null &
echo $! > chainsync.pid

# D（拉 C）
$1nohup ./chainsync $2 >> logs/chainsync.log 2>&1 < /dev/null &
echo $! > chainsync.pid
```

往 A 的 `/data/upload/sync` 放文件，几秒内会依次出现在 B、C、D。

## 参数

| 参数 | 默认 | 说明 |
|------|------|------|
| `-name` | 空（必填） | 节点名 A/B/C/D |
| `-listen` | `:9030` | 监听地址 |
| `-dir` | `/data/upload/sync` | 同步目录 |
| `-upstream` | 空 | 上游 URL，如 `http://A:9030`；空 = 源头 |
| `-interval` | `5s` | 轮询上游间隔 |

## 接口

- `GET /list` → `[{"name","size","mtime"}]`
- `GET /file?name=x` → 文件流，头带 `X-SHA256`、`X-Mtime`，支持 Range
- `GET /health` → 200

## 完整性保证

- 全程流式 SHA-256（32KB 缓冲，内存 O(1)，多 GB 文件也不整块读入）。
- 服务端按 `(name,size,mtime)` 懒计算并缓存哈希，文件变化自动失效。
- 客户端边下载边算哈希，结束后与 `X-SHA256` 比对，不一致删除重下。
- 先写临时文件 `.incoming-*.part`，校验通过后才原子改名，目录里永远只有完整文件。
- 断点续传：中断后下次用 `Range` 从已写字节续传。

## 局限

- 只同步顶层文件，不含子目录。
- 只增量同步（新增/修改），不同步删除。
- 变更判定依据是 `size+mtime`（再配合 SHA-256 校验兜底）。
