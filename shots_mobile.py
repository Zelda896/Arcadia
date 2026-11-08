import os, sys, time, subprocess, tempfile, shutil, urllib.request

ROOT = r"D:\VSCODEfile\arcadia"
OUT = os.path.join(ROOT, "shots")
CHROME = r"C:\Program Files\Google\Chrome\Application\chrome.exe"
PORT = 8097
BASE = "http://127.0.0.1:%d" % PORT

os.makedirs(OUT, exist_ok=True)
work = tempfile.mkdtemp(prefix="arc-mobile-")
content = os.path.join(work, "content")
data = os.path.join(work, "data")
shutil.copytree(os.path.join(ROOT, "content"), content)
os.makedirs(data)
# 多塞几篇，让侧栏有内容可看
demo = [
    ("2026/10/edge-inference", "RK3588 上跑多路视频推理时的内存分配", "2026-10-09 22:14", "网络", ["RK3588"]),
    ("2026/09/openwrt-build", "从零交叉编译一个 OpenWrt 软件包", "2026-09-21 19:02", "网络", ["OpenWrt"]),
    ("2026/08/bbq", "如果记忆可以打包带走", "2026-08-14 01:37", "文章", ["小说"]),
    ("2026/07/git-workflow", "两个仓库：代码一个，内容一个", "2026-07-30 16:20", "杂谈", ["git"]),
    ("2026/03/robot", "循迹小车是怎么调通的", "2026-03-06 23:48", "项目", ["STM32"]),
]
for p, title, date, cat, tags in demo:
    d = os.path.join(content, "posts", p)
    os.makedirs(d, exist_ok=True)
    fm = "---\ntitle: %s\ndate: %s\ncategory: %s\ntags: [%s]\ndraft: false\n---\n\n占位正文。\n" % (title, date, cat, ", ".join(tags))
    open(os.path.join(d, p.split("/")[-1] + ".md"), "w", encoding="utf-8").write(fm)

env = dict(os.environ)
env.update({"ARCADIA_DEV": "1", "ARCADIA_SITE_NAME": "艾嘉蒂娅", "ARCADIA_TAGLINE": "",
            "ARCADIA_BASE_URL": BASE, "ARCADIA_AUTHOR": "Alicia"})

proc = subprocess.Popen([os.path.join(ROOT, "arcadia.exe"), "-addr", "127.0.0.1:%d" % PORT,
                         "-content", content, "-data", data],
                        cwd=ROOT, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def shoot(url, name, w, h):
    ud = tempfile.mkdtemp(prefix="chr-")
    target = os.path.join(OUT, name)
    subprocess.run([
        CHROME, "--headless=new", "--hide-scrollbars", "--no-sandbox",
        "--disable-crash-reporter", "--disable-breakpad", "--disable-dev-shm-usage",
        "--disable-gpu", "--force-device-scale-factor=1",
        "--window-size=%d,%d" % (w, h), "--virtual-time-budget=3000",
        "--user-data-dir=" + ud, "--screenshot=" + target, url,
    ], timeout=90, capture_output=True)
    print("%-20s %s" % (name, "ok" if os.path.exists(target) else "FAIL"), flush=True)


try:
    for _ in range(40):
        try:
            urllib.request.urlopen(BASE + "/", timeout=2)
            break
        except Exception:
            time.sleep(0.25)

    for path, name, w, h in [
        ("/", "m-phone.png", 400, 780),
        ("/", "m-760.png", 760, 900),
        ("/", "m-1090.png", 1090, 900),
        ("/post/how-this-site-works", "m-post-1090.png", 1090, 1000),
        ("/", "m-1440.png", 1440, 900),
        ("/", "m-1920.png", 1920, 1000),
    ]:
        shoot(BASE + path, name, w, h)
finally:
    proc.terminate()
    try:
        proc.wait(timeout=5)
    except Exception:
        proc.kill()
    shutil.rmtree(work, ignore_errors=True)
