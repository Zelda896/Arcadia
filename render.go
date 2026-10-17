package main

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

// Renderer 把 markdown 变成 HTML。
//
// 没有开 html.WithUnsafe()：正文里的原始 HTML 会被转义成文字显示。
// 旧站那些内嵌的 <style> 卡片样式，搬过来就自然失效了——这正是要的效果。
type Renderer struct {
	md goldmark.Markdown
}

func NewRenderer() *Renderer {
	return &Renderer{
		md: goldmark.New(
			goldmark.WithExtensions(extension.GFM, extension.Footnote),
			goldmark.WithRendererOptions(gmhtml.WithHardWraps()),
		),
	}
}

func (r *Renderer) HTML(src []byte) (template.HTML, error) {
	var buf bytes.Buffer
	if err := r.md.Convert(src, &buf); err != nil {
		return "", err
	}
	// 图片默认懒加载：滚动到跟前才下载，手机上打开长文章不至于一上来就拉几十兆
	out := strings.ReplaceAll(buf.String(), "<img ", `<img loading="lazy" `)
	return template.HTML(out), nil
}

var funcMap = template.FuncMap{
	"date": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format("2006-01-02")
	},
	"datetime": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format("2006-01-02 15:04")
	},
	"mmdd": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format("01-02")
	},
	"year": func(t time.Time) string { return t.Format("2006") },
	"join": strings.Join,
}

func (a *App) loadTemplates() error {
	dir, err := fs.Sub(webFS, "web/templates")
	if err != nil {
		return err
	}
	entries, err := fs.ReadDir(dir, ".")
	if err != nil {
		return err
	}
	out := make(map[string]*template.Template, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".html") || name == "layout.html" {
			continue
		}
		// 每个页面单独解析一份 layout，页面里的 {{define "content"}} 不会互相覆盖
		t, err := template.New(name).Funcs(funcMap).ParseFS(dir, "layout.html", name)
		if err != nil {
			return fmt.Errorf("解析模板 %s: %w", name, err)
		}
		out[strings.TrimSuffix(name, ".html")] = t
	}
	a.templates = out
	return nil
}

func (a *App) render(w http.ResponseWriter, name string, d PageData) {
	t, ok := a.templates[name]
	if !ok {
		http.Error(w, "缺少模板 "+name, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", d); err != nil {
		log.Printf("渲染 %s 失败: %v", name, err)
	}
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) {
	w.code = c
	w.ResponseWriter.WriteHeader(c)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.code == 0 {
			sw.code = http.StatusOK
		}
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.code, time.Since(start).Round(time.Millisecond))
	})
}

func withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("panic %s: %v\n%s", r.URL.Path, v, debug.Stack())
				http.Error(w, "服务器内部错误", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func withSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; "+
				"form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}
