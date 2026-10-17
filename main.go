package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed web/templates/*.html web/style.css web/admin.js
var webFS embed.FS

// Site 是从环境变量读来的站点信息。
type Site struct {
	Name    string
	Tagline string
	BaseURL string
	Author  string
}

// App 是全站唯一的运行时对象。
type App struct {
	contentDir string
	dataDir    string
	site       Site
	docs       *DocStore
	comments   *CommentStore
	sessions   *SessionStore
	limiter    *Limiter
	auth       *Auth
	repo       *Repo
	renderer   *Renderer
	templates  map[string]*template.Template
	ipSalt     string
	nav        []*Doc
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "监听地址")
	contentDir := flag.String("content", "./content", "内容目录")
	dataDir := flag.String("data", "./data", "数据目录")
	hashPwd := flag.String("hash-password", "", "生成密码哈希后退出，例如：arcadia -hash-password 你的密码")
	flag.Parse()

	if *hashPwd != "" {
		h, err := HashPassword(*hashPwd)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(h)
		return
	}

	a, err := NewApp(*contentDir, *dataDir)
	if err != nil {
		log.Fatalf("启动失败：%v", err)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           a.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       120 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		log.Printf("http://%s", *addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	go a.watch(30 * time.Second)

	waitForSignal()
	_ = srv.Close()
	log.Println("已停止")
}

func NewApp(contentDir, dataDir string) (*App, error) {
	for _, d := range []string{
		filepath.Join(contentDir, "posts"),
		filepath.Join(contentDir, "pages"),
		filepath.Join(contentDir, "assets"),
		filepath.Join(contentDir, "files"),
		dataDir,
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}

	a := &App{
		contentDir: contentDir,
		dataDir:    dataDir,
		site: Site{
			Name:    env("ARCADIA_SITE_NAME", "Arcadia"),
			Tagline: env("ARCADIA_TAGLINE", ""),
			BaseURL: strings.TrimRight(env("ARCADIA_BASE_URL", "http://localhost:8080"), "/"),
			Author:  env("ARCADIA_AUTHOR", ""),
		},
		renderer: NewRenderer(),
		comments: NewCommentStore(filepath.Join(dataDir, "comments.json")),
		sessions: NewSessionStore(),
		limiter:  NewLimiter(60*time.Second, 3),
		repo:     NewRepo(contentDir),
		ipSalt:   loadSalt(dataDir),
		docs:     NewDocStore(contentDir),
	}

	a.auth = &Auth{User: env("ARCADIA_ADMIN_USER", "admin"), Hash: os.Getenv("ARCADIA_PASSWORD_HASH"), sessions: a.sessions}
	if a.auth.Hash == "" {
		if env("ARCADIA_DEV", "") == "" {
			a.auth.Disabled = true
			log.Println("[警告] 没有配置 ARCADIA_PASSWORD_HASH，后台已禁用")
		} else {
			a.auth.Hash, _ = HashPassword("admin")
			log.Println("[开发模式] 后台口令是 admin")
		}
	}

	if err := a.docs.Load(); err != nil {
		return nil, err
	}
	if err := a.comments.Load(); err != nil {
		return nil, err
	}
	if err := a.loadTemplates(); err != nil {
		return nil, err
	}
	a.refreshNav()
	return a, nil
}

// Routes 里刻意让字面量路径和通配符路径共存：Go 1.22 的 ServeMux
// 会优先匹配更具体的那个，所以 /archive 不会被 /{slug} 抢走。
func (a *App) Routes() http.Handler {
	m := http.NewServeMux()

	m.HandleFunc("GET /{$}", a.handleHome)
	m.HandleFunc("GET /post/{slug}", a.handlePost)
	m.HandleFunc("POST /post/{slug}/comment", a.handleComment)
	m.HandleFunc("GET /archive", a.handleArchive)
	m.HandleFunc("GET /tags/{tag}", a.handleTag)
	m.HandleFunc("GET /style.css", a.handleCSS)
	m.HandleFunc("GET /admin.js", a.handleAdminJS)
	m.HandleFunc("GET /feed.xml", a.handleFeed)
	m.HandleFunc("GET /robots.txt", a.handleRobots)
	m.HandleFunc("GET /assets/{path...}", a.handleAsset)
	m.HandleFunc("GET /files/{path...}", a.handleFile)

	m.HandleFunc("GET /admin/login", a.handleLoginPage)
	m.HandleFunc("POST /admin/login", a.handleLogin)
	m.HandleFunc("POST /admin/logout", a.handleLogout)

	m.Handle("GET /admin", a.auth.Wrap(a.handleAdminHome))
	m.Handle("GET /admin/{$}", a.auth.Wrap(a.handleAdminHome))
	m.Handle("GET /admin/posts", a.auth.Wrap(a.handleAdminList))
	m.Handle("GET /admin/pages", a.auth.Wrap(a.handleAdminList))
	m.Handle("GET /admin/new", a.auth.Wrap(a.handleEditorNew))
	m.Handle("GET /admin/edit", a.auth.Wrap(a.handleEditor))
	m.Handle("POST /admin/save", a.auth.Wrap(a.handleSave))
	m.Handle("POST /admin/delete", a.auth.Wrap(a.handleDelete))
	m.Handle("POST /admin/upload", a.auth.Wrap(a.handleUpload))
	m.Handle("POST /admin/preview", a.auth.Wrap(a.handlePreview))
	m.Handle("GET /admin/comments", a.auth.Wrap(a.handleAdminComments))
	m.Handle("POST /admin/comments/{id}/{act}", a.auth.Wrap(a.handleCommentAction))

	m.HandleFunc("GET /{slug}", a.handlePage)

	return withSecurity(withLogging(withRecover(m)))
}

// refreshNav 重新算一遍导航链接，页面增删后调用。
func (a *App) refreshNav() {
	var nav []*Doc
	for _, p := range a.docs.Pages(false) {
		if p.Nav {
			nav = append(nav, p)
		}
	}
	a.nav = nav
}

func (a *App) watch(d time.Duration) {
	t := time.NewTicker(d)
	defer t.Stop()
	for range t.C {
		if err := a.docs.Reload(); err != nil {
			log.Printf("重扫内容失败: %v", err)
			continue
		}
		a.refreshNav()
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// loadSalt 读或生成 IP 哈希用的盐。必须持久化，否则重启后同一 IP
// 算出来的哈希变了，评论去重就失效。
func loadSalt(dir string) string {
	p := filepath.Join(dir, "salt")
	if b, err := os.ReadFile(p); err == nil && len(b) >= 16 {
		return strings.TrimSpace(string(b))
	}
	s := randomHex(16)
	_ = os.WriteFile(p, []byte(s), 0o600)
	return s
}

func waitForSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
}
