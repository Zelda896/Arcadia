package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

)

// PageData 是发给模板的唯一结构，每个模板各取所需。
type PageData struct {
	Site    Site
	Title   string
	Tagline string
	Year    int
	Nav     []*Doc
	Error   string
	Notice  string

	Groups   []CatGroup
	Posts    []*Doc
	Post     *Doc
	Page     *Doc
	About    *Doc
	Comments []Comment
	Tag      string
	CatName  string
	Tags     []string

	All      []Comment
	Total    int
	Pending  []Comment
	Kind     string
	AllCats  []string
	AdminJS  bool
}

func (a *App) base(title string) PageData {
	if title == "" {
		title = a.site.Name
	} else {
		title = title + " — " + a.site.Name
	}
	return PageData{
		Site: a.site, Title: title, Tagline: a.site.Tagline,
		Nav: a.nav, Year: time.Now().Year(),
	}
}

func (a *App) handleHome(w http.ResponseWriter, r *http.Request) {
	d := a.base("")
	d.About = a.docs.Page("about")
	d.Groups = a.docs.GroupByCategory(false)
	a.render(w, "home", d)
}

func (a *App) handlePost(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	p := a.docs.Post(slug)
	if p == nil || (p.Draft && !a.auth.LoggedIn(r)) {
		http.NotFound(w, r)
		return
	}
	d := a.base(p.Title)
	d.Post = p
	d.Comments = a.comments.ForPost(slug, a.auth.LoggedIn(r))
	switch r.URL.Query().Get("e") {
	case "rate":
		d.Error = "提交太快了，等一分钟再试。"
	case "short":
		d.Error = "名字和内容都填一下。"
	case "dup":
		d.Error = "这条已经发过了。"
	}
	if r.URL.Query().Get("c") == "1" {
		d.Notice = "已提交。"
	}
	a.render(w, "post", d)
}

func (a *App) handlePage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	p := a.docs.Page(slug)
	if p == nil || (p.Draft && !a.auth.LoggedIn(r)) {
		http.NotFound(w, r)
		return
	}
	d := a.base(p.Title)
	d.Page = p
	a.render(w, "page", d)
}

func (a *App) handleCategory(w http.ResponseWriter, r *http.Request) {
	cat := r.PathValue("cat")
	posts := a.docs.ByCategory(cat, false)
	if len(posts) == 0 {
		http.NotFound(w, r)
		return
	}
	d := a.base(cat)
	d.CatName = cat
	d.Posts = posts
	a.render(w, "category", d)
}

func (a *App) handleTag(w http.ResponseWriter, r *http.Request) {
	tag := r.PathValue("tag")
	d := a.base("#" + tag)
	d.Tag = tag
	d.Posts = a.docs.ByTag(tag)
	a.render(w, "tag", d)
}

func (a *App) handleComment(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	p := a.docs.Post(slug)
	if p == nil || (p.Draft && !a.auth.LoggedIn(r)) {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// 蜜罐：真人看不到这个字段，填了就是机器人
	if strings.TrimSpace(r.PostFormValue("website")) != "" {
		http.Redirect(w, r, "/post/"+slug+"?c=1", http.StatusSeeOther)
		return
	}

	author := strings.TrimSpace(r.PostFormValue("author"))
	body := strings.TrimSpace(r.PostFormValue("body"))
	if len([]rune(author)) < 1 || len([]rune(body)) < 2 {
		http.Redirect(w, r, "/post/"+slug+"?e=short", http.StatusSeeOther)
		return
	}
	if len([]rune(author)) > 40 {
		author = string([]rune(author)[:40])
	}
	if len([]rune(body)) > 4000 {
		body = string([]rune(body)[:4000])
	}

	ip := clientIP(r)
	if !a.limiter.Allow("c:" + ip) {
		http.Redirect(w, r, "/post/"+slug+"?e=rate", http.StatusSeeOther)
		return
	}
	iph := hashIP(ip, a.ipSalt)
	if a.comments.Duplicate(slug, iph, body) {
		http.Redirect(w, r, "/post/"+slug+"?e=dup", http.StatusSeeOther)
		return
	}

	if _, err := a.comments.Add(Comment{
		Slug: slug, Author: author, Body: body, IPHash: iph,
		Approved: !looksLikeSpam(body),
	}); err != nil {
		http.Error(w, "保存失败", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/post/"+slug+"?c=1#comments", http.StatusSeeOther)
}

func (a *App) handleAsset(w http.ResponseWriter, r *http.Request) {
	a.serveUnder(w, r, "assets", "public, max-age=31536000, immutable")
}

func (a *App) handleFile(w http.ResponseWriter, r *http.Request) {
	a.serveUnder(w, r, "files", "public, max-age=86400")
}

// serveUnder 提供一个目录下的静态文件，path.Clean 顺手挡掉 ../ 穿越。
func (a *App) serveUnder(w http.ResponseWriter, r *http.Request, sub, cache string) {
	clean := path.Clean("/" + r.PathValue("path"))
	base := filepath.Join(a.contentDir, sub)
	full := filepath.Join(base, filepath.FromSlash(clean))
	if !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", cache)
	http.ServeFile(w, r, full)
}

func (a *App) handleCSS(w http.ResponseWriter, r *http.Request) {
	b, err := webFS.ReadFile("web/style.css")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(b)
}

func (a *App) handleAdminJS(w http.ResponseWriter, r *http.Request) {
	b, err := webFS.ReadFile("web/admin.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	_, _ = w.Write(b)
}

func (a *App) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "User-agent: *\nDisallow: /admin\n")
}

// ---- 登录 ----

func (a *App) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if a.auth.LoggedIn(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	d := a.base("登录")
	if r.URL.Query().Get("e") == "1" {
		d.Error = "用户名或密码不对。"
	}
	a.render(w, "login", d)
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !a.limiter.Allow("login:" + clientIP(r)) {
		http.Redirect(w, r, "/admin/login?e=1", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !a.auth.Check(r.PostFormValue("user"), r.PostFormValue("password")) {
		http.Redirect(w, r, "/admin/login?e=1", http.StatusSeeOther)
		return
	}
	a.auth.Login(w, r)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	a.auth.Logout(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ---- 工具 ----

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func hashIP(ip, salt string) string {
	sum := sha256.Sum256([]byte(ip + salt))
	return hex.EncodeToString(sum[:8])
}

// looksLikeSpam 是个粗糙的启发式：链接太多或命中常见垃圾词就进审核队列。
func looksLikeSpam(body string) bool {
	if strings.Count(body, "http://")+strings.Count(body, "https://") >= 3 {
		return true
	}
	low := strings.ToLower(body)
	for _, w := range []string{"casino", "viagra", "loan", "porn", "博彩", "代开发票", "加微信"} {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
