// 后台编辑器脚本。不到 70 行，没有依赖，没有构建步骤。
(function () {
  'use strict';

  var body = document.getElementById('body');
  var preview = document.getElementById('preview');
  var form = document.getElementById('editor-form');
  var fileInput = document.getElementById('file');

  if (form) {
    document.addEventListener('keydown', function (e) {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
        e.preventDefault();
        form.submit();
      }
    });
  }

  if (body && preview) {
    var timer = null;
    var last = null;
    var render = function () {
      if (body.value === last) return;
      last = body.value;
      var p = new URLSearchParams();
      p.set('body', body.value);
      fetch('/admin/preview', {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: p.toString()
      })
        .then(function (r) { return r.ok ? r.text() : Promise.reject(r.status); })
        .then(function (h) { preview.innerHTML = h; })
        .catch(function () {});
    };
    body.addEventListener('input', function () {
      clearTimeout(timer);
      timer = setTimeout(render, 800);
    });
    render();
  }

  if (fileInput && body) {
    var msg = document.getElementById('upload-msg');

    // 一张一张串行传。服务器不解码图片，但如果浏览器一次并发好几个
    // multipart 请求，磁盘和连接池会同时承受，串行更稳。
    var sendOne = function (files, i, done) {
      if (i >= files.length) {
        msg.textContent = '已插入 ' + done + ' 张。';
        return;
      }
      msg.textContent = '上传中 ' + (i + 1) + ' / ' + files.length + '…';
      var fd = new FormData();
      fd.append('file', files[i]);
      fetch('/admin/upload', { method: 'POST', body: fd })
        .then(function (r) {
          if (!r.ok) return r.text().then(function (t) { throw new Error(t); });
          return r.json();
        })
        .then(function (data) {
          insertAtCursor(body, '\n' + data.files[0].markdown + '\n');
          sendOne(files, i + 1, done + 1);
        })
        .catch(function (err) {
          msg.textContent = '第 ' + (i + 1) + ' 张失败：' + err.message;
        });
    };

    fileInput.addEventListener('change', function () {
      var files = Array.prototype.slice.call(fileInput.files);
      if (!files.length) return;
      fileInput.value = '';
      sendOne(files, 0, 0);
    });
  }

  function insertAtCursor(el, text) {
    var start = el.selectionStart;
    var end = el.selectionEnd;
    el.value = el.value.slice(0, start) + text + el.value.slice(end);
    el.selectionStart = el.selectionEnd = start + text.length;
    el.focus();
    el.dispatchEvent(new Event('input'));
  }

  document.querySelectorAll('form[data-confirm]').forEach(function (f) {
    f.addEventListener('submit', function (e) {
      if (!window.confirm(f.getAttribute('data-confirm'))) e.preventDefault();
    });
  });
})();
