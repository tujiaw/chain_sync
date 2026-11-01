# chainsync —— 链式目录同步工具（拉取模型）

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
./chainsync -name A -listen :9030 -dir /data/upload

# B（拉 A）
./chainsync -name B -listen :9030 -dir /data/upload -upstream http://A主机:9030

# C（拉 B）
./chainsync -name C -listen :9030 -dir /data/upload -upstream http://B主机:9030

# D（拉 C）
./chainsync -name D -listen :9030 -dir /data/upload -upstream http://C主机:9030
```

往 A 的 `/data/upload` 放文件，几秒内会依次出现在 B、C、D。

## 参数

| 参数 | 默认 | 说明 |
|------|------|------|
| `-name` | 空（必填） | 节点名 A/B/C/D |
| `-listen` | `:9030` | 监听地址 |
| `-dir` | `/data/upload` | 同步目录 |
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
