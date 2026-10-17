package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) handleAdminHome(w http.ResponseWriter, r *http.Request) {
	total, _ := a.comments.Count()
	d := a.base("后台")
	d.Posts = a.docs.Posts(true)
	d.Pending = a.comments.Pending()
	d.Total = total
	a.render(w, "admin_home", d)
}

// handleAdminList 同时服务 /admin/posts 和 /admin/pages。
func (a *App) handleAdminList(w http.ResponseWriter, r *http.Request) {
	kind := "post"
	if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/pages") {
		kind = "page"
	}
	d := a.base("内容管理")
	d.Kind = kind
	if kind == "page" {
		d.Posts = a.docs.Pages(true)
	} else {
		d.Posts = a.docs.Posts(true)
	}
	a.render(w, "admin_list", d)
}

func (a *App) handleEditorNew(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind != "page" {
		kind = "post"
	}
	d := a.base("新建")
	d.Kind = kind
	d.Post = &Doc{
		Kind: kind,
		Date: time.Now(),
		Body: "从这里开始写。\n\n段落之间空一行。\n",
	}
	d.AdminJS = true
	a.render(w, "admin_editor", d)
}

func (a *App) handleEditor(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	slug := r.URL.Query().Get("slug")
	var doc *Doc
	if kind == "page" {
		doc = a.docs.Page(slug)
	} else {
		kind = "post"
		doc = a.docs.Post(slug)
	}
	if doc == nil {
		http.NotFound(w, r)
		return
	}
	d := a.base("编辑：" + doc.Title)
	d.Kind = kind
	d.Post = doc
	d.AdminJS = true
	if r.URL.Query().Get("saved") == "1" {
		d.Notice = "已保存。"
	}
	a.render(w, "admin_editor", d)
}

func (a *App) handleSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	kind := r.PostFormValue("kind")
	if kind != "page" {
		kind = "post"
	}
	title := strings.TrimSpace(r.PostFormValue("title"))
	body := r.PostFormValue("body")
	origin := strings.TrimSpace(r.PostFormValue("origin"))
	slug := strings.TrimSpace(r.PostFormValue("slug"))

	if title == "" {
		http.Error(w, "标题不能为空", http.StatusBadRequest)
		return
	}

	// 新建时 slug 可填可空；编辑时禁止改，改了等于换链接
	if origin == "" {
		if slug == "" {
			slug = slugify(title)
		}
		if slug == "" {
			slug = time.Now().Format("20060102-1504")
		}
	} else {
		slug = origin
	}

	doc := &Doc{Slug: slug, Title: title, Body: body, Kind: kind, Date: time.Now()}

	if kind == "post" {
		if t := parseDate(r.PostFormValue("date") + " " + r.PostFormValue("time")); !t.IsZero() {
			doc.Date = t
		} else if t := parseDate(r.PostFormValue("date")); !t.IsZero() {
			doc.Date = t
		}
		for _, t := range strings.Split(r.PostFormValue("tags"), ",") {
			if t = strings.TrimSpace(t); t != "" {
				doc.Tags = append(doc.Tags, t)
			}
		}
	} else {
		doc.Nav = r.PostFormValue("nav") != ""
		if n, err := strconv.Atoi(r.PostFormValue("order")); err == nil {
			doc.Order = n
		}
	}
	doc.Draft = r.PostFormValue("draft") != ""

	if err := a.docs.Save(doc); err != nil {
		http.Error(w, "保存失败："+err.Error(), http.StatusInternalServerError)
		return
	}
	a.refreshNav()

	verb := "更新"
	if origin == "" {
		verb = "新增"
	}
	a.syncContent(verb + "《" + title + "》")

	http.Redirect(w, r, "/admin/edit?kind="+kind+"&slug="+slug+"&saved=1", http.StatusSeeOther)
}

func (a *App) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	kind := r.PostFormValue("kind")
	slug := r.PostFormValue("slug")
	if kind != "page" {
		kind = "post"
	}
	doc := a.docs.Post(slug)
	if kind == "page" {
		doc = a.docs.Page(slug)
	}
	if doc == nil {
		http.NotFound(w, r)
		return
	}
	if err := a.docs.Delete(kind, slug); err != nil {
		http.Error(w, "删除失败："+err.Error(), http.StatusInternalServerError)
		return
	}
	a.refreshNav()
	a.syncContent("删除《" + doc.Title + "》")
	http.Redirect(w, r, "/admin/"+kind+"s", http.StatusSeeOther)
}

// handleUpload 收图原样落盘，返回 markdown 片段给编辑器插入。
func (a *App) handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(24 << 20); err != nil {
		http.Error(w, "文件太大", http.StatusBadRequest)
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		http.Error(w, "没有文件", http.StatusBadRequest)
		return
	}
	if len(files) > 2 {
		http.Error(w, "一次最多 2 张", http.StatusBadRequest)
		return
	}

	type result struct {
		URL      string `json:"url"`
		Markdown string `json:"markdown"`
	}
	out := make([]result, 0, len(files))

	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			http.Error(w, "读取失败", http.StatusBadRequest)
			return
		}
		url, alt, err := a.saveImage(f, fh.Filename, fh.Size)
		f.Close()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out = append(out, result{URL: url, Markdown: "![" + alt + "](" + url + ")"})
	}

	a.syncContent("上传 " + strconv.Itoa(len(out)) + " 张图片")

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"files": out})
}

func (a *App) handlePreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	_, body := splitFrontMatter([]byte(r.PostFormValue("body")))
	h, err := a.renderer.HTML(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(h))
}

func (a *App) handleAdminComments(w http.ResponseWriter, r *http.Request) {
	d := a.base("评论")
	d.All = a.comments.All()
	total, _ := a.comments.Count()
	d.Total = total
	a.render(w, "admin_comments", d)
}

func (a *App) handleCommentAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var e error
	switch r.PathValue("act") {
	case "approve":
		e = a.comments.Approve(id, true)
	case "hide":
		e = a.comments.Approve(id, false)
	case "delete":
		e = a.comments.Delete(id)
	default:
		http.NotFound(w, r)
		return
	}
	if e != nil {
		http.Error(w, e.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/comments", http.StatusSeeOther)
}
