// Package requestlog 内容块存储（blobs + manifests）：
//
// 把每次请求的正文拆成「内容块」按 SHA-256 存文件（同一内容全局只存一份），
// 每条日志本体是一个很小的 manifest JSON（只存元数据 + 块编号列表）。
// 对话场景下第 N 轮请求携带的 N-1 条历史消息全部命中已有块，真正新增只有
// 最后一条消息与响应体——同一段上下文不再被反复整份落库。
//
// 目录结构（与 request-log.db 同级）：
//
//	request-logs/
//	  manifests/<id>.json      日志清单（元数据 + 块编号列表）
//	  blobs/<h2>/<hash>.gz     内容块（gzip 压缩，文件名 = SHA-256 hex）
//
// 删除语义：删日志 = 删 manifest 文件（O(1)，无锁）；块可能被其他日志引用，
// 由 GC 扫描所有 manifest 后删除无引用块（纯文件操作，无 VACUUM 无库锁）。
package requestlog

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// orphanGrace 待认领块的保护期：块已落盘、清单还没写时不能被当成垃圾删掉。
// 正常请求在写块后几毫秒内写清单；这里给 10 分钟，覆盖长流式与磁盘抖动。
// 只保护本进程登记过的 pending 块——上一进程中断遗留的孤儿块可以立即回收。
var orphanGrace = 10 * time.Minute

// blobStore 内容块 + 清单存储。写操作由单把互斥锁串行化。
type blobStore struct {
	dir       string // 根目录（request-logs/）
	blobsDir  string // blobs/
	manifDir  string // manifests/
	mu        sync.Mutex
	knownBlob map[string]bool // 进程内块缓存：写前查重，避免反复 stat 同一文件
	// pending 已落盘但清单还没引用的新块（hash → 写入时刻）。GC 必须跳过它们：
	// 「写块」和「写清单」是两次独立加锁，中间插入的 GC 会把正在记录的正文
	// 当成孤儿删掉。超过 orphanGrace 仍未引用视为放弃的写入，允许回收。
	pending map[string]time.Time
}

// newBlobStore 打开（或初始化）块存储目录。
func newBlobStore(dir string) (*blobStore, error) {
	bs := &blobStore{
		dir:       dir,
		blobsDir:  filepath.Join(dir, "blobs"),
		manifDir:  filepath.Join(dir, "manifests"),
		knownBlob: map[string]bool{},
		pending:   map[string]time.Time{},
	}
	for _, d := range []string{bs.blobsDir, bs.manifDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("request-log: create %s: %w", d, err)
		}
	}
	return bs, nil
}

// contentHash 计算内容的 SHA-256（hex）。
func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// blobPath 块文件路径：blobs/<前2位>/<hash>.gz（两级散列避免单目录文件过多）。
func (bs *blobStore) blobPath(hash string) string {
	return filepath.Join(bs.blobsDir, hash[:2], hash+".gz")
}

// hasBlob 判断块是否已存在（先查内存缓存，未命中再查磁盘并回填缓存）。
func (bs *blobStore) hasBlob(hash string) bool {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	return bs.hasBlobLocked(hash)
}

func (bs *blobStore) hasBlobLocked(hash string) bool {
	if bs.knownBlob[hash] {
		return true
	}
	_, err := os.Stat(bs.blobPath(hash))
	if err != nil {
		return false
	}
	bs.knownBlob[hash] = true
	return true
}

// putBlob 写入内容块（已存在则跳过写入，幂等）。返回内容哈希。
func (bs *blobStore) putBlob(data []byte) (string, error) {
	hash := contentHash(data)
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if bs.hasBlobLocked(hash) {
		return hash, nil
	}
	path := bs.blobPath(hash)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("request-log: create blob dir: %w", err)
	}
	// 临时文件 + rename：写一半进程被杀不会留下半个块（rename 原子）。
	tmp := path + ".tmp"
	if err := writeGzip(tmp, data); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("request-log: rename blob: %w", err)
	}
	bs.knownBlob[hash] = true
	bs.pending[hash] = time.Now()
	return hash, nil
}

// getBlob 读取内容块并解压。
func (bs *blobStore) getBlob(hash string) ([]byte, error) {
	data, err := os.ReadFile(bs.blobPath(hash))
	if err != nil {
		return nil, fmt.Errorf("request-log: read blob %s: %w", hash[:8], err)
	}
	return readGzip(data)
}

// writeManifest 原子写清单文件（临时文件 + rename）。
func (bs *blobStore) writeManifest(id string, m requestManifest) error {
	data, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return fmt.Errorf("request-log: marshal manifest: %w", err)
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	path := bs.manifestPath(id)
	tmp := path + ".tmp"
	if err := writeFileSynced(tmp, data); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("request-log: write manifest: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("request-log: rename manifest: %w", err)
	}
	return nil
}

// writeFileSynced 写入并刷盘后再 rename。断电/磁盘写满时，普通 WriteFile 可能
// 留下全零清单（真实事故：10 份清单变成 128~116KB 的 \x00），整轮 GC 因此停摆。
func writeFileSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// readManifest 读清单。
func (bs *blobStore) readManifest(id string) (requestManifest, error) {
	data, err := os.ReadFile(bs.manifestPath(id))
	if err != nil {
		return requestManifest{}, err
	}
	var m requestManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return requestManifest{}, fmt.Errorf("request-log: parse manifest %s: %w", id, err)
	}
	return m, nil
}

// manifestPath 清单文件路径。
func (bs *blobStore) manifestPath(id string) string {
	return filepath.Join(bs.manifDir, id+".json")
}

// hasManifest 判断清单是否存在。
func (bs *blobStore) hasManifest(id string) bool {
	_, err := os.Stat(bs.manifestPath(id))
	return err == nil
}

// deleteManifest 删除清单文件。返回是否真的删了。
func (bs *blobStore) deleteManifest(id string) bool {
	err := os.Remove(bs.manifestPath(id))
	return err == nil
}

// manifestIDs 列出全部清单 id（GC 与全量操作用）。
func (bs *blobStore) manifestIDs() ([]string, error) {
	entries, err := os.ReadDir(bs.manifDir)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && strings.HasSuffix(name, ".json") {
			ids = append(ids, strings.TrimSuffix(name, ".json"))
		}
	}
	return ids, nil
}

// collectGarbage 块回收：扫描全部 manifest，删除没有任何清单引用的块文件。
// 返回删除的块数与损坏清单 id。纯文件操作，无锁无 VACUUM；悬空引用直接忽略。
//
// 不再因单份清单损坏而整轮放弃：损坏清单改名为 <id>.json.corrupt 隔离保留，
// 同时把它从「有效引用」中剔除——否则一份写坏的文件会让所有孤儿块永久滞留，
// 容量清理只能靠删好日志来「降占用」（真实事故根因）。
// pending 中且未超过 orphanGrace 的块跳过：清单可能还没写完，删了会丢正在
// 进行的请求正文。不按文件 mtime 判断——那样会把刚清理掉的历史块继续留在盘上，
// 让占用看起来没降下来。
func (bs *blobStore) collectGarbage() (removed int, corrupt []string, err error) {
	return bs.collectGarbageGrace(orphanGrace)
}

// collectGarbageGrace 指定孤儿块保护期；grace<=0 表示回收全部无引用块
// （清空日志库时用，新块也已没人要）。
func (bs *blobStore) collectGarbageGrace(grace time.Duration) (removed int, corrupt []string, err error) {
	// 扫描阶段不持锁（大目录可能要几十秒，持锁会卡住所有写日志）。
	// 并发安全靠 pending：putBlob 在锁内登记「已落盘、尚未被清单引用」的块，
	// 删除阶段在锁内复查；清单先于块不可能出现，因此不会误删正在写的正文。
	ids, err := bs.manifestIDs()
	if err != nil {
		return 0, nil, err
	}
	refs := map[string]bool{}
	for _, id := range ids {
		m, err := bs.readManifest(id)
		if err != nil {
			// 隔离损坏清单：文件保留（人工可恢复），但不再参与引用计数。
			if qerr := bs.quarantineManifest(id); qerr == nil {
				corrupt = append(corrupt, id)
			}
			continue
		}
		for _, h := range m.referencedHashes() {
			refs[h] = true
		}
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	removed = 0
	entries, err := os.ReadDir(bs.blobsDir)
	if err != nil {
		return 0, corrupt, err
	}
	for _, e := range entries {
		if !e.IsDir() || len(e.Name()) != 2 {
			continue
		}
		shard := filepath.Join(bs.blobsDir, e.Name())
		files, err := os.ReadDir(shard)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".gz") {
				continue
			}
			hash := strings.TrimSuffix(f.Name(), ".gz")
			if refs[hash] {
				delete(bs.pending, hash) // 已被清单引用：不再需要保护
				continue
			}
			if since, ok := bs.pending[hash]; ok && time.Since(since) < grace {
				continue // 正在写的日志：块已落盘、清单还没写完
			}
			if os.Remove(filepath.Join(shard, f.Name())) == nil {
				removed++
				delete(bs.knownBlob, hash)
			}
		}
		// 空分片顺手清掉
		if rest, err := os.ReadDir(shard); err == nil && len(rest) == 0 {
			_ = os.Remove(shard)
		}
	}
	return removed, corrupt, nil
}

// quarantineManifest 把损坏清单改名隔离，避免每轮 GC 都重复读同一份坏文件。
func (bs *blobStore) quarantineManifest(id string) error {
	src := bs.manifestPath(id)
	dst := src + ".corrupt"
	if _, err := os.Stat(dst); err == nil {
		dst = fmt.Sprintf("%s.corrupt.%d", src, time.Now().Unix())
	}
	return os.Rename(src, dst)
}

// writeGzip 压缩写入文件。
func writeGzip(path string, data []byte) error {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return fmt.Errorf("request-log: gzip write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("request-log: gzip close: %w", err)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// readGzip 解压内容。
func readGzip(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("request-log: gzip reader: %w", err)
	}
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("request-log: gzip read: %w", err)
	}
	return out, nil
}
