package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 构造一个模拟的 ovData 目录，覆盖白名单内外的文件。
func makeFakeData(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"config.json":          `{"system":{"base":{"token":"t"}}}`,
		"server.conf":          "port 1194\nproto udp\n",
		"ovpn.db":              "FAKE-SQLITE-DATA",
		"pki/ca.crt":           "CA CERT",
		"pki/private/ca.key":   "CA PRIVATE KEY",
		"pki/issued/cli1.crt":  "CLIENT CERT",
		"pki/private/cli1.key": "CLIENT PRIVATE KEY",
		"pki/crl.pem":          "CRL",
		"ccd/cli1":             "ifconfig-push 10.8.0.5 255.255.255.0\n",
		"clients/cli1.ovpn":    "client\n<key>PRIVATE</key>\n",
		"clients/stray.log":    "should not be backed up", // 应被过滤
		"web_out.log":          "huge log should not be backed up",
		"web.pid":              "123",
		"openvpn-status.log":   "status",
		".ovc":                 "transient",
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// 往返：备份 → 校验 → 逐文件字节一致，且排除项不在包内。
func TestBackupRoundTrip(t *testing.T) {
	root := makeFakeData(t)
	var buf bytes.Buffer
	if err := writeBackup(root, "1.1.2", &buf); err != nil {
		t.Fatalf("writeBackup: %v", err)
	}

	// 解到空目录并校验
	extract := t.TempDir()
	entries, err := readAndValidateBackup(bytes.NewReader(buf.Bytes()), extract)
	if err != nil {
		t.Fatalf("readAndValidateBackup: %v", err)
	}

	// 用 manifest 收集集合作对比
	want, _, err := collectBackupFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(want) {
		t.Fatalf("entries=%d want=%d", len(entries), len(want))
	}
	for _, e := range want {
		gotPath := filepath.Join(extract, filepath.FromSlash(e.Path))
		b, err := os.ReadFile(gotPath)
		if err != nil {
			t.Fatalf("read extracted %s: %v", e.Path, err)
		}
		orig, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(e.Path)))
		if !bytes.Equal(b, orig) {
			t.Fatalf("content mismatch: %s", e.Path)
		}
	}

	// 排除项不应出现
	for _, bad := range []string{"clients/stray.log", "web_out.log", "web.pid", "openvpn-status.log", ".ovc"} {
		if _, err := os.Stat(filepath.Join(extract, filepath.FromSlash(bad))); !os.IsNotExist(err) {
			t.Fatalf("excluded file leaked into backup: %s", bad)
		}
	}
}

// 私钥类文件在归档内模式应为 0600。
func TestBackupPrivateModes(t *testing.T) {
	root := makeFakeData(t)
	var buf bytes.Buffer
	if err := writeBackup(root, "1.1.2", &buf); err != nil {
		t.Fatal(err)
	}
	gr, _ := gzip.NewReader(bytes.NewReader(buf.Bytes()))
	tr := tar.NewReader(gr)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		private := h.Name == "config.json" || h.Name == "server.conf" || h.Name == "ovpn.db" ||
			strings.HasPrefix(h.Name, "pki/") || strings.HasSuffix(h.Name, ".ovpn")
		if private && h.Mode != 0600 {
			t.Fatalf("private file %s mode=%o, want 0600", h.Name, h.Mode)
		}
	}
}

// 构造一份带指定 manifest 与条目的备份 tar.gz。
func buildTarGz(t *testing.T, mf backupManifest, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	mb, _ := json.Marshal(mf)
	_ = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0644, Size: int64(len(mb)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(mb)
	for name, content := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(content)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(content)
	}
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

// 安全测试：manifest app/schema 不符、SHA 篡改、路径穿越、符号链接条目，均须拒绝。
func TestBackupRejectsInvalid(t *testing.T) {
	cases := []struct {
		name  string
		build func() []byte
	}{
		{
			name: "wrong app",
			build: func() []byte {
				mf := backupManifest{App: "evil", Schema: backupSchema, Files: []backupManifestEntry{}}
				return buildTarGz(t, mf, nil)
			},
		},
		{
			name: "wrong schema",
			build: func() []byte {
				mf := backupManifest{App: backupAppName, Schema: 999, Files: []backupManifestEntry{}}
				return buildTarGz(t, mf, nil)
			},
		},
		{
			name: "sha mismatch",
			build: func() []byte {
				content := []byte("hello")
				mf := backupManifest{App: backupAppName, Schema: backupSchema,
					Files: []backupManifestEntry{{Path: "config.json", Mode: 0644, Size: 5, SHA: strings.Repeat("0", 64)}}}
				return buildTarGz(t, mf, map[string][]byte{"config.json": content})
			},
		},
		{
			name: "path traversal",
			build: func() []byte {
				mf := backupManifest{App: backupAppName, Schema: backupSchema, Files: []backupManifestEntry{}}
				return buildTarGz(t, mf, map[string][]byte{"../../etc/passwd": []byte("x")})
			},
		},
		{
			name: "absolute path",
			build: func() []byte {
				mf := backupManifest{App: backupAppName, Schema: backupSchema, Files: []backupManifestEntry{}}
				return buildTarGz(t, mf, map[string][]byte{"/tmp/evil": []byte("x")})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readAndValidateBackup(bytes.NewReader(tc.build()), t.TempDir())
			if err == nil {
				t.Fatal("expected rejection, got nil")
			}
		})
	}
}

// 合法备份应通过（防止测试把所有包都拒了）。
func TestBackupAcceptsValid(t *testing.T) {
	content := []byte("hello")
	sum := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" // sha256("hello")
	mf := backupManifest{App: backupAppName, Schema: backupSchema,
		Files: []backupManifestEntry{{Path: "config.json", Mode: 0600, Size: 5, SHA: sum}}}
	entries, err := readAndValidateBackup(bytes.NewReader(buildTarGz(t, mf, map[string][]byte{"config.json": content})), t.TempDir())
	if err != nil {
		t.Fatalf("valid backup rejected: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "config.json" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}
