package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// 图片原样存，不解码、不缩放、不重编码。
//
// 这个决定是有意的：go 的 image 包必须把图整个解压进内存才能缩放，
// 一张 4000×3000 的手机照片就是 48 MB，而服务器只有 403 MiB。
// 不解码就没有这个尖峰——文件从网络直接落到磁盘，内存里只有一块 32 KB 的缓冲。
//
// 代价是读者要下载原图，用 loading="lazy" 缓解（滚到才加载）。
const maxImageSize = 20 << 20 // 单张 20 MB

var allowedImage = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// saveImage 边写盘边算哈希，写完再按哈希改名。
// 同一张图重复上传不会产生第二份文件，也顺便绕开中文文件名的编码问题。
func (a *App) saveImage(src io.Reader, origName string, size int64) (url, alt string, err error) {
	if size > maxImageSize {
		return "", "", fmt.Errorf("%s 超过 20 MB", origName)
	}

	head := make([]byte, 512)
	n, rerr := io.ReadFull(src, head)
	if rerr != nil && rerr != io.ErrUnexpectedEOF && rerr != io.EOF {
		return "", "", rerr
	}
	head = head[:n]

	// 用文件头判断真实类型，不信扩展名——防止把 .php 改名成 .jpg 传上来
	ext, ok := allowedImage[http.DetectContentType(head)]
	if !ok {
		return "", "", errors.New("只接受 jpg / png / gif / webp")
	}

	now := time.Now()
	dir := filepath.Join(a.contentDir, "assets", now.Format("2006"), now.Format("01"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}

	tmp, err := os.CreateTemp(dir, ".up-*")
	if err != nil {
		return "", "", err
	}
	cleanup := func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}

	h := sha256.New()
	if _, err := tmp.Write(head); err != nil {
		cleanup()
		return "", "", err
	}
	h.Write(head)

	written, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(src, maxImageSize))
	if err != nil {
		cleanup()
		return "", "", err
	}
	if written+int64(n) > maxImageSize {
		cleanup()
		return "", "", fmt.Errorf("%s 超过 20 MB", origName)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", "", err
	}

	hash := hex.EncodeToString(h.Sum(nil))[:12]
	final := filepath.Join(dir, hash+ext)
	if _, err := os.Stat(final); err == nil {
		os.Remove(tmp.Name()) // 同一张图已经存在
	} else if err := os.Rename(tmp.Name(), final); err != nil {
		os.Remove(tmp.Name())
		return "", "", err
	}

	rel := now.Format("2006") + "/" + now.Format("01") + "/" + hash + ext
	return "/assets/" + rel, stripExt(filepath.Base(origName)), nil
}

func stripExt(name string) string {
	if i := len(name) - len(filepath.Ext(name)); i > 0 {
		return name[:i]
	}
	return name
}
