(function () {
  var currentMode = localStorage.getItem('chartMode') || 'cost';
  var canvasMeta  = new WeakMap();
  var attached    = new Set();

  // ── Helpers ───────────────────────────────────────────────────
  function cv(n) { return getComputedStyle(document.documentElement).getPropertyValue(n).trim(); }

  function colors() {
    return {
      bar: cv('--accent2'), barHi: cv('--accent'),
      text: cv('--text'), text2: cv('--text2'), text3: cv('--text3'),
      border: cv('--border'), bg3: cv('--bg3'),
    };
  }

  function fmtCost(usd) {
    if (!usd || usd < 0.0001) return '$0';
    if (usd < 0.01)  return '<$0.01';
    if (usd >= 1000) return '$' + (usd / 1000).toFixed(1) + 'K';
    if (usd >= 1)    return '$' + usd.toFixed(2);
    return '$' + usd.toFixed(3);
  }

  function humanizeTok(n) {
    if (!n) return '0';
    if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
    if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
    return String(n);
  }

  function getValue(d) { return currentMode === 'cost' ? (d.cost || 0) : (d.tokens || 0); }
  function fmtValue(v) { return currentMode === 'cost' ? fmtCost(v) : humanizeTok(v); }

  function escHtml(s) {
    return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
  }

  // ── Tooltip ───────────────────────────────────────────────────
  function getTooltipEl() { return document.getElementById('chart-tooltip'); }

  function showTooltip(e, d) {
    var tt = getTooltipEl();
    if (!tt) return;
    var html = '<div class="tt-label">' + escHtml(d.label) + '</div>';
    if (d.cost)   html += '<div class="tt-row"><span>비용</span><span class="tt-val">' + fmtCost(d.cost) + '</span></div>';
    if (d.tokens) html += '<div class="tt-row"><span>토큰</span><span class="tt-val">' + humanizeTok(d.tokens) + '</span></div>';
    if (d.count)  html += '<div class="tt-row"><span>세션</span><span class="tt-val">' + d.count + '</span></div>';
    tt.innerHTML = html;
    tt.style.display = 'block';
    moveTooltip(e, tt);
  }

  function moveTooltip(e, tt) {
    var tw = tt.offsetWidth || 150, th = tt.offsetHeight || 80;
    var x = e.clientX + 16, y = e.clientY - 12;
    if (x + tw > window.innerWidth  - 8) x = e.clientX - tw - 16;
    if (y + th > window.innerHeight - 8) y = e.clientY - th - 4;
    tt.style.left = x + 'px';
    tt.style.top  = y + 'px';
  }

  function hideTooltip() {
    var tt = getTooltipEl();
    if (tt) tt.style.display = 'none';
  }

  function attachTooltip(el) {
    if (attached.has(el)) return;
    attached.add(el);
    el.style.cursor = 'default';
    el.addEventListener('mousemove', function(e) {
      var meta = canvasMeta.get(el);
      if (!meta || !meta.data) return;
      var rect = el.getBoundingClientRect();
      var mx = e.clientX - rect.left, my = e.clientY - rect.top;
      var idx = meta.hitTest(mx, my, rect.width, rect.height);
      if (idx >= 0 && idx < meta.data.length) {
        showTooltip(e, meta.data[idx]);
        if (meta.hoveredIdx !== idx) {
          meta.hoveredIdx = idx;
          meta.redraw(idx);
        }
      } else {
        hideTooltip();
        if (meta.hoveredIdx !== -1) { meta.hoveredIdx = -1; meta.redraw(-1); }
      }
    });
    el.addEventListener('mousemove', function(e) {
      var tt = getTooltipEl();
      if (tt && tt.style.display !== 'none') moveTooltip(e, tt);
    });
    el.addEventListener('mouseleave', function() {
      hideTooltip();
      var meta = canvasMeta.get(el);
      if (meta && meta.hoveredIdx !== -1) { meta.hoveredIdx = -1; meta.redraw(-1); }
    });
  }

  // ── Vertical bar chart ────────────────────────────────────────
  function drawVBar(el, data, opts) {
    if (!el || !data || !data.length) return;
    opts = opts || {};
    var hoveredIdx = opts.hoveredIdx !== undefined ? opts.hoveredIdx : -1;

    var dpr = window.devicePixelRatio || 1;
    var w   = el.offsetWidth;
    var h   = el.offsetHeight || parseInt(el.style.height) || 180;
    if (hoveredIdx === -1) { el.width = w * dpr; el.height = h * dpr; }
    var ctx = el.getContext('2d');
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

    var C = colors();
    var padL = 10, padR = 10, padT = 24, padB = 38;
    var cw = w - padL - padR;
    var ch = h - padT - padB;

    var maxVal = 0;
    for (var i = 0; i < data.length; i++) {
      var v = getValue(data[i]);
      if (v > maxVal) maxVal = v;
    }
    if (maxVal === 0) maxVal = 1;

    var barW = cw / data.length;
    var gap  = Math.max(1, barW * 0.2);

    ctx.clearRect(0, 0, w, h);

    // Grid lines
    ctx.strokeStyle = C.border;
    ctx.lineWidth   = 0.5;
    ctx.setLineDash([3, 3]);
    for (var g = 1; g <= 4; g++) {
      var gy = padT + ch * (1 - g / 4);
      ctx.beginPath(); ctx.moveTo(padL, gy); ctx.lineTo(padL + cw, gy); ctx.stroke();
    }
    ctx.setLineDash([]);

    // Max label
    ctx.fillStyle    = C.text3;
    ctx.font         = '11px system-ui,sans-serif';
    ctx.textAlign    = 'right';
    ctx.textBaseline = 'top';
    ctx.fillText(fmtValue(maxVal), padL + cw - 2, padT - 14);

    // Bars
    for (var i = 0; i < data.length; i++) {
      var val = getValue(data[i]);
      var bx  = padL + i * barW + gap / 2;
      var bw2 = barW - gap;
      var bh  = val > 0 ? Math.max(2, (val / maxVal) * ch) : 0;
      var by  = padT + ch - bh;
      var r   = Math.min(3, bw2 / 2, bh);

      var isHi    = opts.highlight ? opts.highlight(data[i], i) : false;
      var isHover = (i === hoveredIdx);
      ctx.fillStyle   = (isHi || isHover) ? C.barHi : C.bar;
      ctx.globalAlpha = (isHi || isHover) ? 1 : 0.7;

      if (bh > 0) {
        ctx.beginPath();
        if (r > 0.5) {
          ctx.moveTo(bx + r, by);
          ctx.lineTo(bx + bw2 - r, by);
          ctx.arcTo(bx + bw2, by,  bx + bw2, by + r,  r);
          ctx.lineTo(bx + bw2, by + bh);
          ctx.lineTo(bx,       by + bh);
          ctx.lineTo(bx,       by + r);
          ctx.arcTo(bx,       by,  bx + r,  by,  r);
        } else {
          ctx.rect(bx, by, bw2, bh);
        }
        ctx.closePath();
        ctx.fill();
      }
      ctx.globalAlpha = 1;
    }

    // X-axis labels
    ctx.fillStyle    = C.text3;
    ctx.textAlign    = 'center';
    ctx.textBaseline = 'bottom';
    var step = Math.ceil(data.length / Math.max(1, Math.floor(cw / 50)));
    for (var i = 0; i < data.length; i++) {
      if (i % step !== 0) continue;
      var fs = Math.max(11, Math.min(13, barW * 0.85));
      ctx.font = fs + 'px system-ui,sans-serif';
      ctx.fillText(data[i].label, padL + (i + 0.5) * barW, h - 6);
    }

    // Store hit-test metadata
    var _padL = padL, _padR = padR, _barW = barW, _data = data;
    canvasMeta.set(el, {
      data:      data,
      hoveredIdx: (canvasMeta.get(el) || {}).hoveredIdx || -1,
      hitTest: function(mx, my, rw) {
        var idx = Math.floor((mx - _padL) / _barW);
        return (idx >= 0 && idx < _data.length) ? idx : -1;
      },
      redraw: function(hIdx) { drawVBar(el, data, { highlight: opts.highlight, hoveredIdx: hIdx }); }
    });
    attachTooltip(el);
  }

  // ── Horizontal bar chart ──────────────────────────────────────
  function drawHBar(el, data) {
    if (!el || !data || !data.length) return;
    var hoveredIdx = (canvasMeta.get(el) || {}).hoveredIdx || -1;

    var dpr  = window.devicePixelRatio || 1;
    var rowH = 36;
    var padT = 8, padB = 8, padR = 72;
    var totalH = data.length * rowH + padT + padB;

    el.style.height = totalH + 'px';
    var w = el.offsetWidth;
    el.width  = w * dpr;
    el.height = totalH * dpr;

    var ctx = el.getContext('2d');
    ctx.scale(dpr, dpr);

    var C      = colors();
    var labelW = Math.min(170, w * 0.33);
    var chartW = w - labelW - padR;

    var maxVal = 0;
    for (var i = 0; i < data.length; i++) {
      var v = getValue(data[i]);
      if (v > maxVal) maxVal = v;
    }
    if (maxVal === 0) maxVal = 1;

    ctx.clearRect(0, 0, w, totalH);

    for (var i = 0; i < data.length; i++) {
      var val = getValue(data[i]);
      var y   = padT + i * rowH;
      var bh  = rowH * 0.5;
      var by  = y + (rowH - bh) / 2;
      var bw  = (val / maxVal) * chartW;
      var isHover = (i === hoveredIdx);

      // Label
      ctx.fillStyle    = isHover ? C.text : C.text2;
      ctx.font         = '14px system-ui,sans-serif';
      ctx.textAlign    = 'right';
      ctx.textBaseline = 'middle';
      var lbl = data[i].label;
      if (lbl.length > 24) lbl = lbl.slice(0, 22) + '…';
      ctx.fillText(lbl, labelW - 10, y + rowH / 2);

      // Bar track
      ctx.fillStyle = C.bg3;
      ctx.fillRect(labelW, by, chartW, bh);

      // Bar fill
      if (bw > 0) {
        var r = Math.min(3, bh / 2, bw);
        ctx.fillStyle   = isHover ? C.barHi : C.bar;
        ctx.globalAlpha = isHover ? 1 : 0.82;
        ctx.beginPath();
        ctx.moveTo(labelW, by);
        ctx.lineTo(labelW + bw - r, by);
        ctx.arcTo(labelW + bw, by,      labelW + bw, by + r,      r);
        ctx.lineTo(labelW + bw, by + bh - r);
        ctx.arcTo(labelW + bw, by + bh, labelW + bw - r, by + bh, r);
        ctx.lineTo(labelW, by + bh);
        ctx.closePath();
        ctx.fill();
        ctx.globalAlpha = 1;
      }

      // Value label
      ctx.fillStyle    = isHover ? C.text : C.text3;
      ctx.textAlign    = 'left';
      ctx.textBaseline = 'middle';
      ctx.font         = '13px system-ui,sans-serif';
      ctx.fillText(fmtValue(val), labelW + bw + 8, y + rowH / 2);
    }

    // Store hit-test metadata
    var _padT = padT, _rowH = rowH, _data = data;
    canvasMeta.set(el, {
      data:      data,
      hoveredIdx: hoveredIdx,
      hitTest: function(mx, my) {
        var idx = Math.floor((my - _padT) / _rowH);
        return (idx >= 0 && idx < _data.length) ? idx : -1;
      },
      redraw: function(hIdx) {
        canvasMeta.set(el, Object.assign(canvasMeta.get(el) || {}, { hoveredIdx: hIdx }));
        drawHBar(el, data);
      }
    });
    attachTooltip(el);
  }

  // ── Render all ────────────────────────────────────────────────
  function renderAll() {
    var d = window.STATS;
    if (!d) return;
    drawVBar(document.getElementById('chart-daily'),    d.daily,   {
      highlight: function(pt, i) { return i === d.daily.length - 1; }
    });
    drawVBar(document.getElementById('chart-monthly'),  d.monthly);
    drawVBar(document.getElementById('chart-weekday'),  d.weekday);
    drawVBar(document.getElementById('chart-hourly'),   d.hourly);
    drawHBar(document.getElementById('chart-models'),   d.models);
    drawHBar(document.getElementById('chart-projects'), d.projects);
  }

  // ── Public API ────────────────────────────────────────────────
  window.setChartMode = function(mode) {
    currentMode = mode;
    localStorage.setItem('chartMode', mode);
    document.querySelectorAll('.mode-btn').forEach(function(btn) {
      btn.classList.toggle('active', btn.dataset.mode === mode);
    });
    renderAll();
  };

  document.addEventListener('DOMContentLoaded', function() {
    document.querySelectorAll('.mode-btn').forEach(function(btn) {
      btn.classList.toggle('active', btn.dataset.mode === currentMode);
    });
    renderAll();
  });
  window.addEventListener('resize', renderAll);
  new MutationObserver(renderAll).observe(document.documentElement, {
    attributes: true, attributeFilter: ['data-theme']
  });
}());
