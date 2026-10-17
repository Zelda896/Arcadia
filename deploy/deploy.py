#!/usr/bin/env python3
"""一条命令把本地代码部署到服务器。

用法（在项目根目录）：
    python deploy/deploy.py

做的事：交叉编译 Linux 版 -> gzip 压缩 -> 上传 -> 解压替换 -> 重启 -> 自检。
只替换二进制，不碰服务器上的 content/ 和 data/。

依赖：pip install paramiko
"""

import gzip
import os
import shutil
import subprocess
import sys
import tempfile
import time

try:
    import paramiko
except ImportError:
    sys.exit("缺少 paramiko：pip install paramiko")

HOST = os.environ.get("ARCADIA_HOST", "47.250.172.205")
USER = os.environ.get("ARCADIA_USER", "root")
KEY = os.path.expanduser(os.environ.get("ARCADIA_KEY", "~/.ssh/arcadia_ed25519"))
REMOTE_BIN = "/opt/arcadia/bin/arcadia"

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "arcadia-linux-amd64")


def build():
    print("[1/5] 交叉编译 Linux 版")
    env = dict(os.environ)
    env.update({
        "CGO_ENABLED": "0",
        "GOOS": "linux",
        "GOARCH": "amd64",
    })
    p = subprocess.run(
        ["go", "build", "-trimpath", "-ldflags=-s -w", "-o", OUT, "."],
        cwd=ROOT, env=env, capture_output=True,
    )
    if p.returncode:
        sys.stderr.write(p.stderr.decode("utf-8", "replace"))
        sys.exit("编译失败")
    print("    %.1f MB" % (os.path.getsize(OUT) / 1048576))


def compress():
    print("[2/5] 压缩")
    tmp = os.path.join(tempfile.gettempdir(), "arcadia.gz")
    with open(OUT, "rb") as f, gzip.open(tmp, "wb", compresslevel=9) as g:
        shutil.copyfileobj(f, g)
    print("    %.1f MB" % (os.path.getsize(tmp) / 1048576))
    return tmp


def connect():
    k = paramiko.Ed25519Key.from_private_key_file(KEY)
    c = paramiko.SSHClient()
    c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    c.connect(HOST, username=USER, pkey=k, timeout=30, look_for_keys=False,
              allow_agent=False, banner_timeout=30, auth_timeout=30)
    c.get_transport().set_keepalive(15)
    return c


def run(c, cmd):
    _, o, e = c.exec_command(cmd, timeout=180)
    out = o.read().decode("utf-8", "replace").rstrip()
    err = e.read().decode("utf-8", "replace").rstrip()
    if err:
        print("    [stderr] " + err)
    return out


def upload(local, attempts=3):
    print("[3/5] 上传")
    for i in range(1, attempts + 1):
        try:
            c = connect()
            run(c, "rm -f %s.gz" % REMOTE_BIN)
            sftp = c.open_sftp()
            sftp.put(local, REMOTE_BIN + ".gz")
            sftp.close()
            size = int(run(c, "stat -c %%s %s.gz" % REMOTE_BIN).strip() or 0)
            if size == os.path.getsize(local):
                print("    第 %d 次成功，远端 %d 字节" % (i, size))
                return c
            print("    第 %d 次大小不符（%d），重试" % (i, size))
            c.close()
        except Exception as ex:
            print("    第 %d 次失败：%s" % (i, ex))
            time.sleep(3)
    sys.exit("上传失败，检查网络或磁盘")


def deploy(c):
    print("[4/5] 解压替换并重启")
    run(c, "cd /opt/arcadia/bin && gunzip -f arcadia.gz && chmod +x arcadia")
    run(c, "systemctl restart arcadia")
    time.sleep(2)
    active = run(c, "systemctl is-active arcadia").strip()
    print("    服务状态: " + active)
    if active != "active":
        print(run(c, "journalctl -u arcadia -n 20 --no-pager"))
        sys.exit("服务没起来")

    print("[5/5] 自检")
    print("    " + run(c, "curl -s -o /dev/null -w '首页 HTTP %{http_code}' http://127.0.0.1:8080/"))
    print("    CSS " + run(c, "curl -s http://127.0.0.1:8080/style.css | wc -l").strip() + " 行")
    print("    " + run(c, "free -m | sed -n 2p").strip())
    c.close()


if __name__ == "__main__":
    build()
    gz = compress()
    c = upload(gz)
    deploy(c)
    print("\n完成。浏览器按 Ctrl+F5 硬刷新，否则可能还在用缓存的旧样式表。")
