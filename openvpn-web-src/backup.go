package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/spf13/viper"
	"gorm.io/gorm"
	gLogger "gorm.io/gorm/logger"
)

// backup.go —— 配置备份 / 一键恢复的流式实现。
//
// 设计要点（见实施计划）：
//   - 备份实时流式生成 tar.gz，不落盘（clients/ 是 /ovpn/download 的 StaticFS，
//     任何落在那里的备份都会把 CA 私钥 / secret_key 暴露出去）。
//   - 只备份核心数据白名单，排除日志 / pid / marker / 瞬态文件。
//   - 恢复包带 manifest（sha256 清单），解包到系统临时目录并逐项校验，
//     防 tar-slip（../、绝对路径、符号链接）与解压炸弹。
//   - 恢复前留一份「恢复前临时快照」.restore-undo.tar.gz（单份，0600），
//     供出错时手工回滚；不做定期快照。

const (
	backupSchema    = 1
	backupAppName   = "openvpn"
	backupMaxTotal  = 200 << 20 // 解压上限 200MB，防解压炸弹
	backupMaxFiles  = 20000
	undoFileName    = ".restore-undo.tar.gz"
	privateFileMode = int64(0600)
)

// backupManifestEntry 是清单中的一个文件条目。
type backupManifestEntry struct {
	Path string `json:"path"` // 相对 ovData 的干净路径，如 "pki/ca.key"
	Mode int64  `json:"mode"` // 归档内权限位（十进制，如 384=0600）
	Size int64  `json:"size"`
	SHA  string `json:"sha256"`
}

type backupManifest struct {
	App       string                `json:"app"`
	Schema    int                   `json:"schema"`
	Version   string                `json:"version"`
	CreatedAt string                `json:"created_at"`
	Files     []backupManifestEntry `json:"files"`
}

// backupTarget 描述一个要备份的相对路径及其类型。
type backupTarget struct {
	rel  string // 相对 ovData 的路径
	dir  bool   // true 表示递归收集目录下所有普通文件
	priv bool   // 该路径下文件按 0600 归档（证书私钥 / config.json / server.conf）
}

// backupTargets 是核心数据白名单。顺序固定，保证备份与校验可复现。
// 排除：日志、*.pid、*.marker、openvpn-status.log、.ovip、.ovc、.resetpw、
// .fixuser、.force_change_pwd、*.nft、config.json.fix、以及 clients/ 下的
// 非 .ovpn 杂项（server.conf / config.json 副本 / *.log）。
var backupTargets = []backupTarget{
	{rel: "config.json", priv: true},
	{rel: "server.conf", priv: true},
	{rel: "ovpn.db", priv: true},
	{rel: "pki", dir: true, priv: true}, // CA 私钥 / tls-crypt key 都必须 0600
	{rel: "ccd", dir: true},
	{rel: "clients", dir: true, priv: true}, // .ovpn 内含客户端私钥，按 0600 保护
}

// isBackupFileName 过滤目录内不需要入包的文件（clients/ 历史遗留的 .conf/.json/.log 等）。
func isBackupFileName(dirRel, name string) bool {
	if strings.HasPrefix(name, ".") { // 排除所有隐藏文件/瞬态文件
		return false
	}
	if dirRel == "clients" && !strings.HasSuffix(name, ".ovpn") {
		return false
	}
	return true
}

// collectBackupFiles 按白名单收集实际存在的文件，返回按路径排序的清单。
func collectBackupFiles(root string) ([]backupManifestEntry, map[string]string, error) {
	var entries []backupManifestEntry
	src := make(map[string]string) // rel -> 磁盘绝对路径
	seen := make(map[string]bool)

	for _, t := range backupTargets {
		abs := filepath.Join(root, t.rel)
		st, err := os.Lstat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				continue // 允许部分文件不存在（如全新安装尚无 clients/）
			}
			return nil, nil, fmt.Errorf("读取 %s 失败: %w", t.rel, err)
		}
		// 拒绝符号链接（防止把外部文件读进备份）
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, nil, fmt.Errorf("备份目标含符号链接，已拒绝: %s", t.rel)
		}
		if !t.dir {
			if !st.Mode().IsRegular() {
				return nil, nil, fmt.Errorf("备份目标不是普通文件: %s", t.rel)
			}
			addBackupFile(abs, t.rel, t.priv, &entries, src, seen)
			continue
		}
		err = filepath.WalkDir(abs, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Type()&os.ModeSymlink != 0 {
					return fmt.Errorf("备份目录含符号链接子目录，已拒绝: %s", p)
				}
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("备份目录含符号链接文件，已拒绝: %s", p)
			}
			if !d.Type().IsRegular() {
				return nil
			}
			name := d.Name()
			if !isBackupFileName(t.rel, name) {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			addBackupFile(p, rel, t.priv, &entries, src, seen)
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, src, nil
}

func addBackupFile(abs, rel string, priv bool, entries *[]backupManifestEntry, src map[string]string, seen map[string]bool) {
	rel = filepath.ToSlash(rel)
	if seen[rel] {
		return
	}
	st, err := os.Stat(abs)
	if err != nil {
		return
	}
	sum := sha256.New()
	f, err := os.Open(abs)
	if err != nil {
		return
	}
	_, _ = io.Copy(sum, f)
	f.Close()

	mode := int64(0644)
	if priv {
		mode = privateFileMode
	}
	*entries = append(*entries, backupManifestEntry{
		Path: rel, Mode: mode, Size: st.Size(), SHA: hex.EncodeToString(sum.Sum(nil)),
	})
	src[rel] = abs
	seen[rel] = true
}

// writeBackup 流式写出完整的 tar.gz 备份到 w。
func writeBackup(root, ver string, w io.Writer) error {
	entries, src, err := collectBackupFiles(root)
	if err != nil {
		return err
	}

	gw := gzip.NewWriter(w)
	tw := tar.NewWriter(gw)

	mf := backupManifest{
		App: backupAppName, Schema: backupSchema, Version: ver,
		CreatedAt: time.Now().UTC().Format(time.RFC3339), Files: entries,
	}
	mfBytes, _ := json.MarshalIndent(mf, "", "  ")
	if err := tw.WriteHeader(&tar.Header{
		Name: "manifest.json", Mode: 0644, Size: int64(len(mfBytes)), Typeflag: tar.TypeReg,
		ModTime: time.Now(),
	}); err != nil {
		tw.Close(); gw.Close()
		return err
	}
	if _, err := tw.Write(mfBytes); err != nil {
		tw.Close(); gw.Close()
		return err
	}

	for _, e := range entries {
		abs := src[e.Path]
		f, err := os.Open(abs)
		if err != nil {
			tw.Close(); gw.Close()
			return fmt.Errorf("打开 %s 失败: %w", e.Path, err)
		}
		st, _ := f.Stat()
		if err := tw.WriteHeader(&tar.Header{
			Name: e.Path, Mode: e.Mode, Size: st.Size(), Typeflag: tar.TypeReg,
			ModTime: st.ModTime(),
		}); err != nil {
			f.Close(); tw.Close(); gw.Close()
			return err
		}
		_, err = io.Copy(tw, f)
		f.Close()
		if err != nil {
			tw.Close(); gw.Close()
			return err
		}
	}

	if err := tw.Close(); err != nil {
		gw.Close()
		return err
	}
	return gw.Close()
}

// readAndValidateBackup 从 r 读取备份，安全解到 emptyDir，校验后返回解出的文件清单。
func readAndValidateBackup(r io.Reader, emptyDir string) ([]backupManifestEntry, error) {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("不是有效的 gzip 备份: %w", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)

	var mf *backupManifest
	var entries []backupManifestEntry
	total := int64(0)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("读取备份失败: %w", err)
		}
		name := filepath.ToSlash(hdr.Name)
		// 防 tar-slip：拒绝绝对路径、..、以及任何非普通文件（含符号链接/硬链接/目录在此一并拒绝）。
		if name == "" || filepath.IsAbs(name) || strings.HasPrefix(name, "../") ||
			strings.Contains(name, "/../") || strings.HasPrefix(name, "/") ||
			hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("备份包含非法条目: %q", hdr.Name)
		}
		if len(entries) >= backupMaxFiles {
			return nil, fmt.Errorf("备份文件数超过上限 %d", backupMaxFiles)
		}
		total += hdr.Size
		if total > backupMaxTotal {
			return nil, fmt.Errorf("备份解压大小超过上限 %dMB", backupMaxTotal>>20)
		}

		if name == "manifest.json" {
			if mf != nil {
				return nil, fmt.Errorf("备份含多个 manifest.json")
			}
			b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return nil, err
			}
			var m backupManifest
			if json.Unmarshal(b, &m) != nil {
				return nil, fmt.Errorf("manifest.json 解析失败")
			}
			if m.App != backupAppName || m.Schema != backupSchema {
				return nil, fmt.Errorf("备份类型或版本不兼容（app=%q schema=%d）", m.App, m.Schema)
			}
			mf = &m
			continue
		}

		// 在校验 manifest 前先落盘到临时目录（manifest 在 tar 首项，正常情况下已读到）。
		dst := filepath.Join(emptyDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return nil, err
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, fmt.Errorf("写入临时文件失败: %w", err)
		}
		sum := sha256.New()
		_, err = io.Copy(io.MultiWriter(out, sum), tr)
		cerr := out.Close()
		if err != nil || cerr != nil {
			return nil, fmt.Errorf("解包失败: %v", err)
		}
		entries = append(entries, backupManifestEntry{
			Path: name, Mode: hdr.Mode, Size: hdr.Size, SHA: hex.EncodeToString(sum.Sum(nil)),
		})
	}

	if mf == nil {
		return nil, fmt.Errorf("备份缺少 manifest.json")
	}
	if len(mf.Files) != len(entries) {
		return nil, fmt.Errorf("备份文件数与清单不符（清单 %d，实际 %d）", len(mf.Files), len(entries))
	}

	manifestMap := make(map[string]backupManifestEntry, len(mf.Files))
	for _, e := range mf.Files {
		manifestMap[e.Path] = e
	}
	for _, e := range entries {
		want, ok := manifestMap[e.Path]
		if !ok {
			return nil, fmt.Errorf("备份含清单外的文件: %s", e.Path)
		}
		if want.SHA != e.SHA || want.Size != e.Size {
			return nil, fmt.Errorf("备份文件校验失败（SHA256 不符）: %s", e.Path)
		}
	}

	// 最终顺序以 manifest 为准（恢复时按 manifest 顺序写）。
	return mf.Files, nil
}

// makePreRestoreSnapshot 把当前核心数据打包成 ovData/.restore-undo.tar.gz（单份、0600）。
// 这是恢复操作的临时安全网，不是定期快照。
func makePreRestoreSnapshot(ver string) error {
	path := filepath.Join(ovData, undoFileName)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("创建恢复前快照失败: %w", err)
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(tmp)
		}
	}()
	if err := writeBackup(ovData, ver, f); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	_ = os.Chmod(path, 0600)
	ok = true
	return nil
}

// applyRestore 把校验过的临时目录文件覆盖回 ovData。
// 调用方负责在恢复前调用 makePreRestoreSnapshot，并在恢复后 reload 服务。
func applyRestore(emptyDir string, entries []backupManifestEntry) error {
	// 1) 关闭 SQLite 活连接——不关闭的话覆盖 ovpn.db 不生效（旧 inode 仍被持有）。
	if db != nil {
		if sqlDB, err := db.DB(); err == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
		db = nil
	}
	dbOK := false
	defer func() {
		if !dbOK {
			// 尽力恢复 DB 连接，避免应用彻底不可用。
			reopenDB()
		}
	}()

	// 2) 覆盖文件（原地写 O_TRUNC，保证 viper WatchConfig 能捕获 config.json 变更）。
	for _, e := range entries {
		rel := filepath.FromSlash(e.Path)
		src := filepath.Join(emptyDir, rel)
		dst := filepath.Join(ovData, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		mode := os.FileMode(e.Mode)
		if mode == 0 {
			mode = 0644
		}
		if err := os.WriteFile(dst, b, mode); err != nil {
			return fmt.Errorf("恢复 %s 失败: %w", e.Path, err)
		}
		_ = os.Chmod(dst, mode)
	}

	// 3) 重新打开 DB + 重新加载配置（不依赖异步 fsnotify）。
	if err := reopenDB(); err != nil {
		return err
	}
	dbOK = true
	if err := viper.ReadInConfig(); err != nil {
		return fmt.Errorf("重新读取 config.json 失败: %w", err)
	}
	loadConfig()
	return nil
}

// reopenDB 重新打开 ovpn.db（与 main.go:782 同一 DSN）。
func reopenDB() error {
	var err error
	db, err = gorm.Open(sqlite.Open(filepath.Join(ovData, "ovpn.db")+"?_pragma=foreign_keys(1)"), &gorm.Config{
		Logger: gLogger.Default.LogMode(gLogger.Silent),
	})
	return err
}

// backupInfo 返回给 UI 的备份概览（不泄露文件内容）。
type backupInfo struct {
	Files      int    `json:"files"`
	Bytes      int64  `json:"bytes"`
	HasUndo    bool   `json:"hasUndo"`
	UndoTime   string `json:"undoTime,omitempty"`
	Version    string `json:"version"`
	DataDir    string `json:"dataDir"`    // 应用数据目录（ovData），告知用户快照/数据实际位置
	UndoPath   string `json:"undoPath,omitempty"` // 恢复前快照完整路径（存在时）
}

func getBackupInfo() backupInfo {
	info := backupInfo{Version: version, DataDir: ovData}
	entries, _, err := collectBackupFiles(ovData)
	if err == nil {
		info.Files = len(entries)
		for _, e := range entries {
			info.Bytes += e.Size
		}
	}
	if st, err := os.Stat(filepath.Join(ovData, undoFileName)); err == nil {
		info.HasUndo = true
		info.UndoTime = st.ModTime().Format("2006-01-02 15:04:05")
		info.UndoPath = filepath.Join(ovData, undoFileName)
	}
	return info
}
