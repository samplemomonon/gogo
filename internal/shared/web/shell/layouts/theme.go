package layouts

// ThemeBootstrapJS runs inline, in <head>, before the stylesheet is applied,
// so the resolved theme is on <html> for the very first paint. Loading it as an
// external file instead would cost a round trip and flash the wrong theme.
//
// The shell deliberately does not hardcode `data-theme` on the server: leaving
// the attribute off is what lets the stylesheet's
// `@media (prefers-color-scheme: dark) { :root:not([data-theme]) }` rule supply
// dark mode to a viewer whose OS asks for it. This script only pins the
// attribute when the reader has expressed a preference of their own, so an
// undecided reader keeps following their OS.
//
// Storage is wrapped in try/catch because localStorage throws outright in
// Safari private mode and when storage is blocked by policy; a failed read
// degrades to "follow the OS" rather than breaking the page.
const ThemeBootstrapJS = `
(function () {
  var root = document.documentElement;
  var key = "gogo-theme";
  var themes = ["gogo", "gogo-dark"];

  function stored() {
    try {
      var value = window.localStorage.getItem(key);
      return themes.indexOf(value) === -1 ? null : value;
    } catch (e) {
      return null;
    }
  }

  function systemTheme() {
    return window.matchMedia &&
      window.matchMedia("(prefers-color-scheme: dark)").matches
      ? "gogo-dark"
      : "gogo";
  }

  var theme = stored();
  if (theme) {
    root.setAttribute("data-theme", theme);
  }

  // Public to the theme toggle, which lives in the body and is rendered after
  // this script runs.
  window.__gogoTheme = {
    get: function () {
      return root.getAttribute("data-theme") || systemTheme();
    },
    set: function (next) {
      if (themes.indexOf(next) === -1) {
        return;
      }
      // Swap instantly. Changing data-theme restyles every colour token in the
      // document at once; with transitions live, each of those starts its own
      // transition and the page spends a few hundred milliseconds repainting
      // itself out of step. Suppress them for the frame in which the new
      // values are computed, then hand control back. Two frames, not one: the
      // first commits the new theme, the second guarantees the browser has
      // discarded the suppressed style before the flag comes off, so nothing
      // is left mid-transition.
      root.setAttribute("data-theme-switching", "");
      root.setAttribute("data-theme", next);
      requestAnimationFrame(function () {
        requestAnimationFrame(function () {
          root.removeAttribute("data-theme-switching");
        });
      });
      try {
        window.localStorage.setItem(key, next);
      } catch (e) {
        /* Preference is not persistable here; it still applies to this page. */
      }
      window.dispatchEvent(new CustomEvent("gogo:themechange", { detail: next }));
    },
    toggle: function () {
      this.set(this.get() === "gogo-dark" ? "gogo" : "gogo-dark");
    }
  };
})();
`
