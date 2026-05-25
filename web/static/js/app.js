const html = document.documentElement;

// ── Theme ──────────────────────────────────────────────
function applyTheme(theme) {
  html.setAttribute('data-theme', theme);
  const btn = document.getElementById('btn-theme');
  if (btn) btn.textContent = theme === 'dark' ? '☀' : '☾';
}

function toggleTheme() {
  const next = html.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
  localStorage.setItem('theme', next);
  applyTheme(next);
}

// ── Font size ──────────────────────────────────────────
function applyFont(size) {
  html.style.setProperty('--font-size-base', size + 'px');
  const slider = document.getElementById('font-slider');
  if (slider) slider.value = size;
  const label = document.getElementById('font-size-val');
  if (label) label.textContent = size + 'px';
}

function setFont(size) {
  localStorage.setItem('fontSize', size);
  applyFont(size);
}

// ── Init ───────────────────────────────────────────────
document.addEventListener('DOMContentLoaded', () => {
  applyTheme(localStorage.getItem('theme') || 'dark');

  const savedSize = parseInt(localStorage.getItem('fontSize') || '16');
  applyFont(savedSize);

  const slider = document.getElementById('font-slider');
  if (slider) {
    slider.addEventListener('input', () => setFont(parseInt(slider.value)));
  }

  // Highlight active nav link
  const path = location.pathname;
  document.querySelectorAll('.nav-link').forEach(a => {
    const href = a.getAttribute('href');
    if (href === '/' ? path === '/' : path.startsWith(href)) {
      a.style.background = 'var(--bg3)';
      a.style.color = 'var(--text)';
    }
  });
});
