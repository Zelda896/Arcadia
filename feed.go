package main

import (
	"encoding/xml"
	"net/http"
	"strings"
	"time"
)

type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Language    string    `xml:"language"`
	LastBuild   string    `xml:"lastBuildDate"`
	Items       []rssItem `xml:"item"`
}

type rssItem struct {
	Title   string `xml:"title"`
	Link    string `xml:"link"`
	GUID    string `xml:"guid"`
	PubDate string `xml:"pubDate"`
	Desc    string `xml:"description,omitempty"`
}

func (a *App) handleFeed(w http.ResponseWriter, r *http.Request) {
	posts := a.docs.Posts(false)
	if len(posts) > 30 {
		posts = posts[:30]
	}

	feed := rssFeed{
		Version: "2.0",
		Channel: rssChannel{
			Title:       a.site.Name,
			Link:        a.site.BaseURL,
			Description: a.site.Tagline,
			Language:    "zh-CN",
			LastBuild:   time.Now().Format(time.RFC1123Z),
		},
	}
	for _, p := range posts {
		link := a.site.BaseURL + p.URL()
		feed.Channel.Items = append(feed.Channel.Items, rssItem{
			Title:   p.Title,
			Link:    link,
			GUID:    link,
			PubDate: p.Date.Format(time.RFC1123Z),
			Desc:    excerpt(p.Body, 180),
		})
	}

	b, err := xml.MarshalIndent(feed, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(b)
}

// excerpt 从 markdown 原文剥一段纯文本，够阅读器展示就行。
func excerpt(md string, n int) string {
	var b strings.Builder
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "---") ||
			strings.HasPrefix(t, "![") || strings.HasPrefix(t, "```") || strings.HasPrefix(t, "|") {
			continue
		}
		b.WriteString(t)
		b.WriteString(" ")
		if len([]rune(b.String())) > n {
			break
		}
	}
	out := []rune(strings.TrimSpace(b.String()))
	if len(out) > n {
		return string(out[:n]) + "…"
	}
	return string(out)
}
