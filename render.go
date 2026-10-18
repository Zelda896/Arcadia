package main

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// Renderer 把 markdown 变成 HTML。
//
// 没有开 html.WithUnsafe()：正文里的原始 HTML 会被转义成文字显示。
// 旧站那些内嵌的 <style> 卡片样式，搬过来就自然失效了——这正是要的效果。
type Renderer struct {
	md goldmark.Markdown
}

// seqIDs 给标题按出现顺序编号：s1、s2、s3……
//
// 不用 goldmark 自带的 parser.WithAutoHeadingID()，因为它只保留 ASCII，
// 所有中文标题都会退化成 heading / heading-1 / heading-2，而且插入一个新
// 标题会让后面全部错位，目录里点哪一条都跳错地方。用序号就没这个问题。
type seqIDs struct {
	n    int
	used map[string]bool
}

func (s *seqIDs) Generate(value []byte, kind ast.NodeKind) []byte {
	for {
		s.n++
		id := "s" + strconv.Itoa(s.n)
		if !s.used[id] {
			s.used[id] = true
			return []byte(id)
		}
	}
}

func (s *seqIDs) Put(value []byte) { s.used[string(value)] = true }

// tocItem 是目录里的一条。
type tocItem struct {
	Level int
	ID    string
	Title string
}

// buildTOC 扫一遍文档里的二级、三级标题，拼成嵌套列表。
//
// 没用 go.abhg.dev/goldmark/toc：那个库的层级栈是从 h1 算起的
// （stack 初始只有一个根项，遇到 h2 会先垫一个空项），而我们的文章标题
// 在模板里、正文从 ## 开始，结果所有 h2 都被塞成第一个 h2 的子项，
// tree.Items 永远是 1 条。它没提供配置改这个假设，逻辑又只有几十行，
// 不如自己写。
func (r *Renderer) buildTOC(doc ast.Node, src []byte) template.HTML {
	var items []tocItem
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		h, ok := n.(*ast.Heading)
		if !ok || h.Level < 2 || h.Level > 3 {
			return ast.WalkContinue, nil
		}
		id := ""
		if v, ok := h.AttributeString("id"); ok {
			if b, ok := v.([]byte); ok {
				id = string(b)
			}
		}
		items = append(items, tocItem{Level: h.Level, ID: id, Title: string(h.Text(src))})
		return ast.WalkSkipChildren, nil
	})

	// 只有一条的目录没有意义
	if len(items) < 2 {
		return ""
	}

	base := items[0].Level
	cur := base
	var b strings.Builder
	b.WriteString(`<ul>`)
	for i, it := range items {
		if i > 0 {
			switch {
			case it.Level > cur:
				for ; cur < it.Level; cur++ {
					b.WriteString(`<ul>`)
				}
			default:
				for ; cur > it.Level; cur-- {
					b.WriteString(`</li></ul>`)
				}
				b.WriteString(`</li>`)
			}
		}
		b.WriteString(`<li><a href="#` + it.ID + `">`)
		b.WriteString(template.HTMLEscapeString(it.Title))
		b.WriteString(`</a>`)
	}
	for ; cur > base; cur-- {
		b.WriteString(`</li></ul>`)
	}
	b.WriteString(`</li></ul>`)
	return template.HTML(b.String())
}

func NewRenderer() *Renderer {
	return &Renderer{
		md: goldmark.New(
			goldmark.WithExtensions(extension.GFM, extension.Footnote),
			// 这个选项负责"要不要给标题生成 id"，具体怎么生成由每次
			// 渲染时喂进去的 seqIDs 决定（默认那个只认 ASCII，中文会全丢）。
			goldmark.WithParserOptions(parser.WithAutoHeadingID()),
			goldmark.WithRendererOptions(gmhtml.WithHardWraps()),
		),
	}
}

// Render 返回正文 HTML 和目录 HTML。目录只收二级和三级标题；
// 一篇文章里的二级标题少于两个时不生成目录，免得短文章旁边挂个孤零零的一条。
func (r *Renderer) Render(src []byte) (body template.HTML, nav template.HTML, err error) {
	ids := &seqIDs{used: map[string]bool{}}
	pctx := parser.NewContext(parser.WithIDs(ids))
	doc := r.md.Parser().Parse(text.NewReader(src), parser.WithContext(pctx))
	nav = r.buildTOC(doc, src)

	var buf bytes.Buffer
	if err := r.md.Renderer().Render(&buf, src, doc); err != nil {
		return "", "", err
	}
	// 图片默认懒加载：滚动到跟前才下载，手机上打开长文章不至于一上来就拉几十兆
	out := strings.ReplaceAll(buf.String(), "<img ", `<img loading="lazy" `)
	return template.HTML(out), nav, nil
}

// HTML 只取正文，给评论用——评论不需要目录。
func (r *Renderer) HTML(src []byte) (template.HTML, error) {
	body, _, err := r.Render(src)
	return body, err
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
