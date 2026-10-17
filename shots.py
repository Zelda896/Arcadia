import os, time, subprocess, tempfile, shutil, urllib.request

ROOT = r"D:\VSCODEfile\arcadia"
OUT = os.path.join(ROOT, "shots")
CHROME = r"C:\Program Files\Google\Chrome\Application\chrome.exe"
PORT = 8095
BASE = "http://127.0.0.1:%d" % PORT
GO = r"C:\Users\11612\.workbuddy\binaries\goroot\bin\go.exe"

env = dict(os.environ)
env.update({
    "GOPATH": r"C:\Users\11612\.workbuddy\gopath",
    "GOCACHE": r"C:\Users\11612\.workbuddy\gocache",
    "GOPROXY": "https://goproxy.cn,direct",
    "GOTOOLCHAIN": "local",
})
p = subprocess.run([GO, "build", "-o", "arcadia.exe", "."], cwd=ROOT, env=env, capture_output=True)
print("build:", p.returncode, p.stderr.decode("utf-8", "replace")[-400:])
if p.returncode != 0:
    raise SystemExit(1)

os.makedirs(OUT, exist_ok=True)
work = tempfile.mkdtemp(prefix="arc-shot-")
content = os.path.join(work, "content")
data = os.path.join(work, "data")
shutil.copytree(os.path.join(ROOT, "content"), content)
os.makedirs(data)

demo = [
    ("2026/10/edge-inference", "RK3588 上跑多路视频推理时的内存分配", "2026-10-09 22:14", ["RK3588", "嵌入式"]),
    ("2026/09/openwrt-build", "从零交叉编译一个 OpenWrt 软件包", "2026-09-21 19:02", ["OpenWrt"]),
    ("2026/08/tcp-congestion", "在丢包 3% 的链路上，BBR 到底比 CUBIC 好在哪", "2026-08-14 01:37", ["网络"]),
    ("2026/03/esp32-lowpower", "ESP32 深睡眠模式下那 12 微安是怎么省出来的", "2026-03-06 23:48", ["ESP32"]),
    ("2025/12/first-post", "开始记点东西", "2025-12-02 10:05", []),
]
for p_, title, date, tags in demo:
    d = os.path.join(content, "posts", p_)
    os.makedirs(d, exist_ok=True)
    fm = "---\ntitle: %s\ndate: %s\n" % (title, date)
    if tags:
        fm += "tags: [%s]\n" % ", ".join(tags)
    fm += "draft: false\n---\n\n占位正文。\n"
    open(os.path.join(d, p_.split("/")[-1] + ".md"), "w", encoding="utf-8").write(fm)

senv = dict(os.environ)
senv.update({
    "ARCADIA_DEV": "1",
    "ARCADIA_SITE_NAME": "艾嘉蒂娅",
    "ARCADIA_TAGLINE": "散落在时空里的记忆",
    "ARCADIA_BASE_URL": BASE,
    "ARCADIA_AUTHOR": "Alicia",
})

proc = subprocess.Popen(
    [os.path.join(ROOT, "arcadia.exe"), "-addr", "127.0.0.1:%d" % PORT,
     "-content", content, "-data", data],
    cwd=ROOT, env=senv, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
)

try:
    for _ in range(40):
        try:
            urllib.request.urlopen(BASE + "/", timeout=2)
            break
        except Exception:
            time.sleep(0.25)

    for path, name, size in [("/", "wide-home.png", "1600,1000"),
                             ("/post/how-this-site-works", "wide-post.png", "1600,1300"),
                             ("/about", "wide-page.png", "1600,800")]:
        ud = tempfile.mkdtemp(prefix="chr-")
        target = os.path.join(OUT, name)
        subprocess.run([
            CHROME, "--headless=new", "--hide-scrollbars", "--no-sandbox",
            "--disable-crash-reporter", "--disable-breakpad", "--disable-dev-shm-usage",
            "--disable-gpu", "--force-device-scale-factor=1",
            "--window-size=" + size, "--virtual-time-budget=2500",
            "--user-data-dir=" + ud, "--screenshot=" + target, BASE + path,
        ], timeout=90, capture_output=True)
        print(name, "ok" if os.path.exists(target) else "FAIL")
finally:
    proc.terminate()
    try:
        proc.wait(timeout=5)
    except Exception:
        proc.kill()
    shutil.rmtree(work, ignore_errors=True)
