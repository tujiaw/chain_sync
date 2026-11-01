// chainsync: 链式目录同步工具（拉取模型）
//
// 每个节点同时是服务端(供下游拉取)和客户端(轮询上游)。
// 访问方向固定为下游访问上游：B 拉 A、C 拉 B、D 拉 C。
//
// 用法：
//
//	A: chainsync -name A -listen :9030 -dir /data/upload
//	B: chainsync -name B -listen :9030 -dir /data/upload -upstream http://A:9030
//	C: chainsync -name C -listen :9030 -dir /data/upload -upstream http://B:9030
//	D: chainsync -name D -listen :9030 -dir /data/upload -upstream http://C:9030
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Config 一个节点的运行配置。
type Config struct {
	Name     string        // 节点名 A/B/C/D
	Listen   string        // 监听地址，如 :9030
	Dir      string        // 同步目录，如 /data/upload
	Upstream string        // 上游节点 URL，如 http://A:9030；空 = 源头节点
	Interval time.Duration // 轮询上游的间隔
}

// FileInfo 是 /list 返回的目录清单项。
type FileInfo struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"` // UnixNano
}

// Node 一个同步节点。
type Node struct {
	cfg       Config
	cli       *http.Client
	hashCache sync.Map // key: "name|size|mtime" -> sha256 hex
}

func main() {
	name := flag.String("name", "", "节点名 A/B/C/D")
	listen := flag.String("listen", ":9030", "监听地址")
	dir := flag.String("dir", "/data/upload", "同步目录")
	upstream := flag.String("upstream", "", "上游节点 URL，如 http://A:9030；空=源头")
	interval := flag.Duration("interval", 5*time.Second, "轮询上游间隔")
	flag.Parse()

	if *name == "" {
		log.Fatal("必须用 -name 指定节点名，例如 -name B")
	}
	if *interval <= 0 {
		log.Fatal("-interval 必须大于 0")
	}

	n := &Node{
		cfg: Config{
			Name:     *name,
			Listen:   *listen,
			Dir:      *dir,
			Upstream: strings.TrimRight(*upstream, "/"),
			Interval: *interval,
		},
		cli: newHTTPClient(),
	}

	if err := os.MkdirAll(n.cfg.Dir, 0o755); err != nil {
		log.Fatalf("创建目录失败: %v", err)
	}

	// 服务端：供下游拉取
	mux := http.NewServeMux()
	mux.HandleFunc("/list", n.handleList)
	mux.HandleFunc("/file", n.handleFile)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{Addr: n.cfg.Listen, Handler: mux}
	go func() {
		log.Printf("[%s] 监听 %s, 目录=%s, upstream=%q, interval=%s",
			n.cfg.Name, n.cfg.Listen, n.cfg.Dir, n.cfg.Upstream, n.cfg.Interval)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务错误: %v", err)
		}
	}()

	// 客户端：轮询上游
	if n.cfg.Upstream != "" {
		go n.pollLoop()
	} else {
		log.Printf("[%s] 无上游，作为源头节点，仅提供文件服务", n.cfg.Name)
	}

	// 等待退出信号
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	log.Printf("[%s] 收到退出信号，正在关闭...", n.cfg.Name)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

// newHTTPClient 构造一个适合大文件传输的客户端：
// 不设置整体 Timeout（否则多 GB 文件会被打断），
// 只对建连、TLS 握手、响应头到达设置超时。
func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 5 * time.Minute, // 首次下载时服务端要算哈希，放宽到 5 分钟
			IdleConnTimeout:       90 * time.Second,
		},
	}
}

// handleList 返回目录文件清单（跳过子目录和点开头临时文件）。
func (n *Node) handleList(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(n.cfg.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	list := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, FileInfo{Name: e.Name(), Size: info.Size(), Mtime: info.ModTime().UnixNano()})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// handleFile 下载单个文件，附带 X-SHA256（完整文件哈希）和 X-Mtime 头。
func (n *Node) handleFile(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if !sanitizeName(name) {
		http.Error(w, "非法文件名", http.StatusBadRequest)
		return
	}
	path := filepath.Join(n.cfg.Dir, name)
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 懒计算 + 缓存哈希：文件一变(size/mtime)缓存键自动失效。
	sum, ok := n.hashCache.Load(fileKey(name, info))
	if !ok {
		s, err := hashFile(f)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// 复查：若哈希期间文件又被改写，重算一次。
		if st2, err := f.Stat(); err == nil && (st2.Size() != info.Size() || st2.ModTime() != info.ModTime()) {
			if s2, err := hashFile(f); err == nil {
				s, info = s2, st2
			}
		}
		sum = s
		n.hashCache.Store(fileKey(name, info), sum)
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-SHA256", sum.(string))
	w.Header().Set("X-Mtime", fmt.Sprintf("%d", info.ModTime().UnixNano()))
	http.ServeContent(w, r, name, info.ModTime(), f) // 自带 Range，配合断点续传
}

// pollLoop 周期性轮询上游。
func (n *Node) pollLoop() {
	for {
		if err := n.pollOnce(); err != nil {
			log.Printf("[%s] 轮询 %s 失败: %v", n.cfg.Name, n.cfg.Upstream, err)
		}
		time.Sleep(n.cfg.Interval)
	}
}

// pollOnce 拉取上游清单，逐文件比对并下载缺失/不一致的文件。
func (n *Node) pollOnce() error {
	up, err := n.fetchList(n.cfg.Upstream + "/list")
	if err != nil {
		return err
	}
	for _, f := range up {
		if !sanitizeName(f.Name) {
			log.Printf("[%s] 忽略上游返回的非法文件名: %q", n.cfg.Name, f.Name)
			continue
		}
		local := filepath.Join(n.cfg.Dir, f.Name)
		info, err := os.Stat(local)
		if err == nil && info.Size() == f.Size && info.ModTime().UnixNano() == f.Mtime {
			continue // 一致，跳过
		}
		if err := n.download(f); err != nil {
			log.Printf("[%s] 下载 %s 失败: %v", n.cfg.Name, f.Name, err)
		}
	}
	return nil
}

func (n *Node) fetchList(u string) ([]FileInfo, error) {
	resp, err := n.cli.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上游返回 %s", resp.Status)
	}
	var list []FileInfo
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list, nil
}

// download 下载单个文件：流式写盘 + 流式哈希校验 + 断点续传。
func (n *Node) download(f FileInfo) error {
	tmpName := filepath.Join(n.cfg.Dir, ".incoming-"+f.Name+".part")

	// 已有未完成的部分文件 → 从已写字节续传
	var offset int64
	if st, err := os.Stat(tmpName); err == nil {
		offset = st.Size()
	}

	tmp, err := os.OpenFile(tmpName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}

	u := n.cfg.Upstream + "/file?name=" + url.QueryEscape(f.Name)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		tmp.Close()
		return err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := n.cli.Do(req)
	if err != nil {
		tmp.Close()
		return err
	}
	defer resp.Body.Close()

	if offset > 0 && resp.StatusCode == http.StatusOK {
		// 上游不支持 Range，丢弃旧部分重下
		if err := tmp.Truncate(0); err != nil {
			tmp.Close()
			return err
		}
		offset = 0
	} else if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		tmp.Close()
		return fmt.Errorf("上游返回 %s", resp.Status)
	}

	// 边写边算哈希：io.Copy 用 32KB 缓冲，内存 O(1)，与文件大小无关
	h := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, h), resp.Body)
	tmp.Close()
	if err != nil {
		return err
	}
	if offset+written != f.Size {
		return fmt.Errorf("大小不一致: 期望 %d, 实际 %d", f.Size, offset+written)
	}

	// 校验哈希
	wantSum := resp.Header.Get("X-SHA256")
	var gotSum string
	if offset == 0 {
		gotSum = hex.EncodeToString(h.Sum(nil)) // 全新下载，哈希即全文件哈希
	} else {
		gotSum, err = fileSHA256(tmpName) // 续传：需对整个文件重算一次
		if err != nil {
			return err
		}
	}

	if wantSum != "" && gotSum != wantSum {
		os.Remove(tmpName) // 校验失败删掉，下轮重下
		return fmt.Errorf("SHA256 校验失败")
	}

	mtimeNs := f.Mtime
	if v := resp.Header.Get("X-Mtime"); v != "" {
		fmt.Sscanf(v, "%d", &mtimeNs)
	}
	mtime := time.Unix(0, mtimeNs)
	if err := os.Chtimes(tmpName, mtime, mtime); err != nil {
		os.Remove(tmpName)
		return err
	}

	dst := filepath.Join(n.cfg.Dir, f.Name)
	if err := renameOverwrite(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return err
	}
	log.Printf("[%s] 同步 %s (%d 字节, sha256=%s)", n.cfg.Name, f.Name, offset+written, gotSum)
	return nil
}

// hashFile 流式计算整个文件的 SHA-256，读完回到文件开头。
func hashFile(f *os.File) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fileSHA256 打开文件并流式计算其 SHA-256。
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileKey(name string, info os.FileInfo) string {
	return fmt.Sprintf("%s|%d|%d", name, info.Size(), info.ModTime().UnixNano())
}

// sanitizeName 校验文件名是否安全：纯文件名、无路径分隔符、非 "." ".."、非点开头。
// 防止上游(被劫持/伪造时)通过 "../" 越界删除或写入同步目录之外的文件。
func sanitizeName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return false
	}
	return filepath.Base(name) == name
}

// renameOverwrite 覆盖式重命名：os.Rename 在 Windows 下不能覆盖已存在文件，做个兼容。
func renameOverwrite(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	_ = os.Remove(dst)
	return os.Rename(src, dst)
}
