package main

import (
	"encoding/json"
	"html/template"
	"os"
	"sort"
	"sync"
	"time"
)

// ---- 评论 ----

type Comment struct {
	ID        int64         `json:"id"`
	Slug      string        `json:"slug"`
	Author    string        `json:"author"`
	Body      string        `json:"body"`
	CreatedAt time.Time     `json:"created_at"`
	IPHash    string        `json:"ip_hash"`
	Approved  bool          `json:"approved"`
	HTML      template.HTML `json:"-"`
}

// CommentStore 把评论全量放内存、写时整份落盘。
//
// 个人博客几千条评论的量级下这完全够用，换来的是可以直接 cat、
// 可以进 git 做 diff、不用养一个数据库进程。写操作在进程内加锁串行化。
type CommentStore struct {
	mu     sync.RWMutex
	file   string
	seq    int64
	items  []Comment
	render *Renderer
}

func NewCommentStore(file string) *CommentStore {
	return &CommentStore{file: file, render: NewRenderer()}
}

func (s *CommentStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.file)
	if os.IsNotExist(err) || len(raw) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	var items []Comment
	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}
	for i := range items {
		if h, err := s.render.HTML([]byte(items[i].Body)); err == nil {
			items[i].HTML = h
		}
		if items[i].ID > s.seq {
			s.seq = items[i].ID
		}
	}
	s.items = items
	return nil
}

func (s *CommentStore) flush() error {
	raw, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.file, raw, 0o644)
}

func (s *CommentStore) Add(c Comment) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	c.ID = s.seq
	c.CreatedAt = time.Now()
	if h, err := s.render.HTML([]byte(c.Body)); err == nil {
		c.HTML = h
	}
	s.items = append(s.items, c)
	if err := s.flush(); err != nil {
		s.items = s.items[:len(s.items)-1]
		s.seq--
		return 0, err
	}
	return c.ID, nil
}

// Duplicate 拦掉"同一 IP 在同一篇下发一模一样的话"。
func (s *CommentStore) Duplicate(slug, ipHash, body string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.items {
		if c.Slug == slug && c.IPHash == ipHash && c.Body == body {
			return true
		}
	}
	return false
}

func (s *CommentStore) ForPost(slug string, includePending bool) []Comment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Comment
	for _, c := range s.items {
		if c.Slug != slug || (!c.Approved && !includePending) {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (s *CommentStore) All() []Comment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Comment, len(s.items))
	copy(out, s.items)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (s *CommentStore) Pending() []Comment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Comment
	for _, c := range s.items {
		if !c.Approved {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (s *CommentStore) Count() (total, pending int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total = len(s.items)
	for _, c := range s.items {
		if !c.Approved {
			pending++
		}
	}
	return
}

func (s *CommentStore) Approve(id int64, ok bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID == id {
			s.items[i].Approved = ok
			return s.flush()
		}
	}
	return ErrNotFound
}

func (s *CommentStore) Delete(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID == id {
			s.items = append(s.items[:i], s.items[i+1:]...)
			return s.flush()
		}
	}
	return ErrNotFound
}

// ---- 会话 ----

// SessionStore 只放内存。进程重启后台会掉一次线，对单人博客不算问题，
// 换来的是不用操心 token 落盘的权限问题。
type SessionStore struct {
	mu     sync.RWMutex
	tokens map[string]time.Time
	ttl    time.Duration
}

func NewSessionStore() *SessionStore {
	return &SessionStore{tokens: map[string]time.Time{}, ttl: 14 * 24 * time.Hour}
}

func (s *SessionStore) New() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok := randomHex(32)
	s.tokens[tok] = time.Now().Add(s.ttl)
	return tok
}

func (s *SessionStore) Valid(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.RLock()
	exp, ok := s.tokens[tok]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		s.Delete(tok)
		return false
	}
	return true
}

func (s *SessionStore) Delete(tok string) {
	s.mu.Lock()
	delete(s.tokens, tok)
	s.mu.Unlock()
}

// ---- 限流 ----

// Limiter 是内存里的滑动窗口，防止有人在几分钟内刷几百条评论。
type Limiter struct {
	mu     sync.Mutex
	window time.Duration
	max    int
	hits   map[string][]time.Time
	gc     time.Time
}

func NewLimiter(window time.Duration, max int) *Limiter {
	return &Limiter{window: window, max: max, hits: map[string][]time.Time{}, gc: time.Now()}
}

func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if now.Sub(l.gc) > 10*time.Minute {
		for k, v := range l.hits {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > l.window {
				delete(l.hits, k)
			}
		}
		l.gc = now
	}

	kept := make([]time.Time, 0, len(l.hits[key]))
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}
