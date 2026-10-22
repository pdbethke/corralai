// SPDX-License-Identifier: Elastic-2.0
// Site chrome controls: theme toggle, site search + replay speed, wired across
// the sticky header (#sh-theme-toggle, #sh-replay-speed) and the cockpit replay bar
// (#replay-theme-toggle, #replay-speed). All elements marked with
// data-site-theme-toggle / data-replay-speed stay in sync and persist to
// localStorage (corralai-site-theme, corralai-replay-speed).
(function () {
  var THEME_KEY = 'corralai-site-theme';
  var COCKPIT_KEY = 'corralai-cockpit-theme';
  var SPEED_KEY = 'corralai-replay-speed';
  var SPEEDS = [1, 2, 4, 8, 16];
  var DEFAULT_SPEED = 2;
  function storedSpeed() {
    try {
      var n = Number(localStorage.getItem(SPEED_KEY));
      return SPEEDS.indexOf(n) >= 0 ? n : DEFAULT_SPEED;
    } catch (e) { return DEFAULT_SPEED; }
  }
  function themeToggles() {
    return document.querySelectorAll('[data-site-theme-toggle]');
  }
  function cockpitToggles() {
    return document.querySelectorAll('[data-cockpit-theme-toggle]');
  }
  function speedSelects() {
    return document.querySelectorAll('[data-replay-speed]');
  }
  function paintThemeIcons() {
    var dark = document.documentElement.getAttribute('data-theme') === 'dark';
    themeToggles().forEach(function (b) {
      var icon = b.querySelector('.sh-tt-icon, .rbc-tt-icon');
      if (icon) icon.textContent = dark ? '☀️' : '🌙';
    });
  }
  // The demo window's OWN light/dark — separate from the site theme above.
  // Stamps data-cockpit-theme on :root (drives the --stage-* token family) and
  // asks the player to repaint the canvas, which caches its colors.
  function paintCockpitIcons() {
    var light = document.documentElement.getAttribute('data-cockpit-theme') === 'light';
    cockpitToggles().forEach(function (b) {
      var icon = b.querySelector('.rbc-ct-icon');
      if (icon) icon.textContent = light ? '☀️' : '🌙';
    });
  }
  function syncSpeedSelects(v) {
    speedSelects().forEach(function (sel) {
      sel.value = String(v);
    });
  }
  function wire() {
    paintThemeIcons();
    themeToggles().forEach(function (b) {
      b.addEventListener('click', function () {
        var next = document.documentElement.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
        document.documentElement.setAttribute('data-theme', next);
        try { localStorage.setItem(THEME_KEY, next); } catch (e) {}
        paintThemeIcons();
      });
    });
    paintCockpitIcons();
    cockpitToggles().forEach(function (b) {
      b.addEventListener('click', function () {
        var next = document.documentElement.getAttribute('data-cockpit-theme') === 'light' ? 'dark' : 'light';
        document.documentElement.setAttribute('data-cockpit-theme', next);
        try { localStorage.setItem(COCKPIT_KEY, next); } catch (e) {}
        paintCockpitIcons();
        if (typeof window.refreshStageColors === 'function') window.refreshStageColors();
      });
    });
    var initial = storedSpeed();
    syncSpeedSelects(initial);
    speedSelects().forEach(function (speed) {
      speed.addEventListener('change', function () {
        var v = Number(this.value);
        syncSpeedSelects(v);
        try { localStorage.setItem(SPEED_KEY, String(v)); } catch (e) {}
        if (typeof window.setReplaySpeed === 'function') window.setReplaySpeed(v);
      });
    });
  }
  // Site search: the header's button opens its <dialog>, and Pagefind's UI
  // (/pagefind/, written next to the built site) is loaded on the first open,
  // so a page that is never searched never fetches it. / and Ctrl/Cmd+K open
  // it too. Under `astro dev` there is no index; the dialog says so.
  var searchUI = null;
  function openSearch() {
    var dlg = document.querySelector('[data-site-search]');
    if (!dlg) return;
    if (!dlg.open) dlg.showModal();
    if (searchUI) { focusSearch(dlg); return; }
    searchUI = 'loading';
    var css = document.createElement('link');
    css.rel = 'stylesheet';
    css.href = '/pagefind/pagefind-ui.css';
    document.head.appendChild(css);
    var js = document.createElement('script');
    js.src = '/pagefind/pagefind-ui.js';
    js.onload = function () {
      searchUI = new window.PagefindUI({ element: '#site-search-ui', showSubResults: true, showImages: false, resetStyles: false });
      focusSearch(dlg);
    };
    js.onerror = function () {
      searchUI = null;
      var ui = document.getElementById('site-search-ui');
      if (ui) ui.textContent = 'Search is not available here: the index is built with the site (npm run build).';
    };
    document.head.appendChild(js);
  }
  function focusSearch(dlg) {
    var input = dlg.querySelector('input');
    if (input) input.focus();
  }
  function wireSearch() {
    var dlg = document.querySelector('[data-site-search]');
    if (!dlg) return;
    document.querySelectorAll('[data-site-search-open]').forEach(function (b) {
      b.addEventListener('click', openSearch);
    });
    document.querySelectorAll('[data-site-search-close]').forEach(function (b) {
      b.addEventListener('click', function () { dlg.close(); });
    });
    // A click on the backdrop lands on the dialog itself, outside its box.
    dlg.addEventListener('click', function (e) { if (e.target === dlg) dlg.close(); });
    document.addEventListener('keydown', function (e) {
      var typing = /^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName) || e.target.isContentEditable;
      if ((e.key === 'k' && (e.ctrlKey || e.metaKey)) || (e.key === '/' && !typing)) {
        e.preventDefault();
        openSearch();
      }
    });
  }
  // The docs nav (DocsSidebar.astro): below 1024px a drawer the header's Docs
  // button opens, by data-site-side on <html>; Escape, a click outside it or
  // a link in it closes it. At any width the current page is scrolled into
  // the nav's view, so a page deep in the list shows where it is.
  function wireSide() {
    var side = document.getElementById('site-side');
    if (!side) return;
    var root = document.documentElement;
    var toggles = document.querySelectorAll('[data-site-side-toggle]');
    function set(open) {
      if (open) root.setAttribute('data-site-side', 'open');
      else root.removeAttribute('data-site-side');
      toggles.forEach(function (b) { b.setAttribute('aria-expanded', open ? 'true' : 'false'); });
    }
    var isOpen = function () { return root.getAttribute('data-site-side') === 'open'; };
    toggles.forEach(function (b) {
      b.addEventListener('click', function (e) { e.stopPropagation(); set(!isOpen()); });
    });
    document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && isOpen()) set(false); });
    document.addEventListener('click', function (e) {
      if (isOpen() && (!side.contains(e.target) || e.target.closest('a'))) set(false);
    });
    var here = side.querySelector('a[aria-current="page"]');
    if (here) side.scrollTop = here.offsetTop - side.clientHeight / 3;
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', wireSearch);
  else wireSearch();
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', wireSide);
  else wireSide();
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', wire);
  else wire();
})();
