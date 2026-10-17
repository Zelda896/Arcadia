package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Repo 负责把内容目录的改动提交回 GitHub。
//
// 它是可选组件：content 不是 git 仓库、或者机器上没装 git，就直接不启用。
// 同步失败只写日志，绝不影响发布——文件已经落盘，网站已经能看到了，
// 版本记录晚几分钟补上没有任何关系。
type Repo struct {
	dir     string
	enabled bool
	mu      sync.Mutex
}

func NewRepo(contentDir string) *Repo {
	r := &Repo{dir: contentDir}
	if _, err := os.Stat(filepath.Join(contentDir, ".git")); err != nil {
		log.Println("[git] content 不是 git 仓库，自动提交关闭")
		return r
	}
	if _, err := exec.LookPath("git"); err != nil {
		log.Println("[git] 找不到 git，自动提交关闭")
		return r
	}
	r.enabled = true
	log.Println("[git] 自动提交已开启")
	return r
}

func (r *Repo) Enabled() bool { return r.enabled }

func (r *Repo) run(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.CombinedOutput()
}

// Sync 串行执行，因为后台可能连续触发多次（比如一口气传几张图）。
func (r *Repo) Sync(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 先 rebase，否则本地有提交时 push 会被远端拒绝
	if out, err := r.run("pull", "--rebase", "--autostash", "--quiet", "origin", "HEAD"); err != nil {
		log.Printf("[git] pull 失败，继续本地提交: %v %s", err, out)
	}
	if out, err := r.run("add", "-A"); err != nil {
		log.Printf("[git] add 失败: %v %s", err, out)
		return
	}
	out, err := r.run("commit", "-m", msg)
	if err != nil {
		var exit *exec.ExitError
		// 没有改动时 commit 返回 1，这不是错误
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return
		}
		log.Printf("[git] commit 失败: %v %s", err, out)
		return
	}
	if out, err := r.run("push", "--quiet", "origin", "HEAD"); err != nil {
		log.Printf("[git] push 失败（改动已提交到本地，下次会重试）: %v %s", err, out)
		return
	}
	log.Printf("[git] 已同步: %s", msg)
}

func (a *App) syncContent(msg string) {
	if a.repo == nil || !a.repo.Enabled() {
		return
	}
	go a.repo.Sync(msg)
}
