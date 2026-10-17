package main

import (
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

var ErrNotFound = errors.New("找不到")

// Doc 同时代表文章和独立页面。两者格式一样，只是存放目录和 URL 不同：
// content/posts/2026/10/xxx.md → /post/xxx
// content/pages/xxx.md         → /xxx
type Doc struct {
	Slug  string
	Title string
	Date  time.Time
	Tags  []string
	Draft bool
	Nav   bool // 是否出现在顶部导航
	Order int  // 导航排序，小的在前
	Body  string
	HTML  template.HTML
	Path  string
	Kind  string // post / page
}

func (d *Doc) URL() string {
	if d.Kind == "page" {
		return "/" + d.Slug
	}
	return "/post/" + d.Slug
}

// Raw 还原成带 front matter 的原文，给编辑框用。
func (d *Doc) Raw() string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("title: " + d.Title + "\n")
	if d.Kind == "post" {
		b.WriteString("date: " + d.Date.Format("2006-01-02 15:04") + "\n")
		if len(d.Tags) > 0 {
			b.WriteString("tags: [" + strings.Join(d.Tags, ", ") + "]\n")
		}
	}
	if d.Draft {
		b.WriteString("draft: true\n")
	}
	if d.Kind == "page" {
		if d.Nav {
			b.WriteString("nav: true\n")
		}
		if d.Order != 0 {
			b.WriteString(fmt.Sprintf("order: %d\n", d.Order))
		}
	}
	b.WriteString("---\n\n")
	b.WriteString(d.Body)
	return b.String()
}

type DocStore struct {
	mu         sync.RWMutex
	dir        string
	render     *Renderer
	posts      []*Doc
	pages      []*Doc
	postBySlug map[string]*Doc
	pageBySlug map[string]*Doc
	stamps     map[string]time.Time
}

func NewDocStore(contentDir string) *DocStore {
	return &DocStore{
		dir:        contentDir,
		render:     NewRenderer(),
		postBySlug: map[string]*Doc{},
		pageBySlug: map[string]*Doc{},
		stamps:     map[string]time.Time{},
	}
}

func (s *DocStore) Load() error {
	var posts, pages []*Doc
	postBySlug := map[string]*Doc{}
	pageBySlug := map[string]*Doc{}
	stamps := map[string]time.Time{}

	scan := func(sub, kind string) {
		root := filepath.Join(s.dir, sub)
		_ = filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
				return nil
			}
			d, err := s.parse(p, kind)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[跳过] %s: %v\n", p, err)
				return nil
			}
			if fi, err := e.Info(); err == nil {
				stamps[p] = fi.ModTime()
			}
			if kind == "post" {
				if _, dup := postBySlug[d.Slug]; dup {
					fmt.Fprintf(os.Stderr, "[跳过] slug 重复: %s\n", p)
					return nil
				}
				postBySlug[d.Slug] = d
				posts = append(posts, d)
			} else {
				if _, dup := pageBySlug[d.Slug]; dup {
					fmt.Fprintf(os.Stderr, "[跳过] slug 重复: %s\n", p)
					return nil
				}
				pageBySlug[d.Slug] = d
				pages = append(pages, d)
			}
			return nil
		})
	}
	scan("posts", "post")
	scan("pages", "page")

	sort.Slice(posts, func(i, j int) bool {
		if posts[i].Date.Equal(posts[j].Date) {
			return posts[i].Slug < posts[j].Slug
		}
		return posts[i].Date.After(posts[j].Date)
	})
	sort.Slice(pages, func(i, j int) bool {
		if pages[i].Order != pages[j].Order {
			return pages[i].Order < pages[j].Order
		}
		return pages[i].Title < pages[j].Title
	})

	s.mu.Lock()
	s.posts, s.pages = posts, pages
	s.postBySlug, s.pageBySlug = postBySlug, pageBySlug
	s.stamps = stamps
	s.mu.Unlock()
	return nil
}

// Reload 只在文件 mtime 变过时才重扫，用来响应后台保存和 git pull。
func (s *DocStore) Reload() error {
	s.mu.RLock()
	prev := s.stamps
	s.mu.RUnlock()

	seen := map[string]bool{}
	changed := false
	for _, sub := range []string{"posts", "pages"} {
		_ = filepath.WalkDir(filepath.Join(s.dir, sub), func(p string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
				return nil
			}
			seen[p] = true
			if fi, err := e.Info(); err == nil {
				if old, ok := prev[p]; !ok || !old.Equal(fi.ModTime()) {
					changed = true
				}
			}
			return nil
		})
	}
	for p := range prev {
		if !seen[p] {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.Load()
}

func (s *DocStore) parse(path, kind string) (*Doc, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	meta, body := splitFrontMatter(raw)

	var fm struct {
		Title string `yaml:"title"`
		Date  string `yaml:"date"`
		Tags  []any  `yaml:"tags"`
		Draft bool   `yaml:"draft"`
		Nav   bool   `yaml:"nav"`
		Order int    `yaml:"order"`
	}
	if len(meta) > 0 {
		if err := yaml.Unmarshal(meta, &fm); err != nil {
			return nil, fmt.Errorf("front matter: %w", err)
		}
	}

	slug := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	title := strings.TrimSpace(fm.Title)
	if title == "" {
		title = slug
	}

	date := parseDate(fm.Date)
	if date.IsZero() {
		if fi, err := os.Stat(path); err == nil {
			date = fi.ModTime()
		} else {
			date = time.Now()
		}
	}

	var tags []string
	for _, t := range fm.Tags {
		if v, ok := t.(string); ok && strings.TrimSpace(v) != "" {
			tags = append(tags, strings.TrimSpace(v))
		}
	}

	h, err := s.render.HTML(body)
	if err != nil {
		return nil, err
	}

	return &Doc{
		Slug: slug, Title: title, Date: date, Tags: tags,
		Draft: fm.Draft, Nav: fm.Nav, Order: fm.Order,
		Body: string(body), HTML: h, Path: path, Kind: kind,
	}, nil
}

func (s *DocStore) Save(d *Doc) error {
	if d.Slug == "" {
		return errors.New("slug 不能为空")
	}
	var dir string
	if d.Kind == "page" {
		dir = filepath.Join(s.dir, "pages")
	} else {
		dir = filepath.Join(s.dir, "posts", d.Date.Format("2006"), d.Date.Format("01"))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, d.Slug+".md")

	s.mu.RLock()
	var old *Doc
	if d.Kind == "page" {
		old = s.pageBySlug[d.Slug]
	} else {
		old = s.postBySlug[d.Slug]
	}
	s.mu.RUnlock()
	if old != nil && old.Path != path {
		_ = os.Remove(old.Path)
	}

	if err := writeFileAtomic(path, []byte(d.Raw()), 0o644); err != nil {
		return err
	}
	d.Path = path
	return s.Load()
}

func (s *DocStore) Delete(kind, slug string) error {
	s.mu.RLock()
	var d *Doc
	if kind == "page" {
		d = s.pageBySlug[slug]
	} else {
		d = s.postBySlug[slug]
	}
	s.mu.RUnlock()
	if d == nil {
		return ErrNotFound
	}
	if err := os.Remove(d.Path); err != nil {
		return err
	}
	return s.Load()
}

func (s *DocStore) Post(slug string) *Doc {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.postBySlug[slug]
}

func (s *DocStore) Page(slug string) *Doc {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pageBySlug[slug]
}

func (s *DocStore) Posts(includeDraft bool) []*Doc {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Doc, 0, len(s.posts))
	for _, d := range s.posts {
		if d.Draft && !includeDraft {
			continue
		}
		out = append(out, d)
	}
	return out
}

func (s *DocStore) Pages(includeDraft bool) []*Doc {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Doc, 0, len(s.pages))
	for _, d := range s.pages {
		if d.Draft && !includeDraft {
			continue
		}
		out = append(out, d)
	}
	return out
}

func (s *DocStore) ByTag(tag string) []*Doc {
	var out []*Doc
	for _, d := range s.Posts(false) {
		for _, t := range d.Tags {
			if strings.EqualFold(t, tag) {
				out = append(out, d)
				break
			}
		}
	}
	return out
}

func (s *DocStore) Tags() []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range s.Posts(false) {
		for _, t := range d.Tags {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ---- 工具 ----

func splitFrontMatter(raw []byte) (meta, body []byte) {
	s := string(raw)
	if !strings.HasPrefix(s, "---") {
		return nil, raw
	}
	i := strings.IndexByte(s, '\n')
	if i < 0 {
		return nil, raw
	}
	rest := s[i+1:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, raw
	}
	return []byte(rest[:end]), []byte(strings.TrimPrefix(rest[end+4:], "\n"))
}

var dateLayouts = []string{
	"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02",
	time.RFC3339, "2006/01/02 15:04", "2006/01/02",
}

func parseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, l := range dateLayouts {
		if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify 只保留 ASCII。中文标题一般会得到空串，调用方要兜底成日期。
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
	}
	return strings.Trim(s, "-")
}

// writeFileAtomic 先写临时文件再 rename，进程被杀也不会留下半个文件。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	return os.Rename(name, path)
}
