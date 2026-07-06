// SPDX-License-Identifier: Elastic-2.0
// Site chrome controls: theme toggle + replay speed, wired across the sticky
// header (#sh-theme-toggle, #sh-replay-speed) and the cockpit replay bar
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
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', wire);
  else wire();
})();
