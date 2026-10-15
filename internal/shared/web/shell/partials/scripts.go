package partials

// ThemeApplyJS binds the theme toggle to the bootstrap API published by
// layouts.ThemeBootstrapJS. It ships with the toggle rather than the bootstrap
// script because it needs both the button and that API, and only one of them
// exists before the body is parsed.
const ThemeApplyJS = `
(function () {
  function paint(button) {
    var dark = window.__gogoTheme.get() === "gogo-dark";
    // Named after the action the button performs, not the current state, so
    // the label is still true after the click. This has to be Indonesian: it
    // overwrites the toggle's static aria-label, so an English string here puts
    // English in the accessible name of a control whose markup is Indonesian.
    var label = dark ? "Ganti ke tema terang" : "Ganti ke tema gelap";
    button.setAttribute("aria-pressed", String(dark));
    button.setAttribute("aria-label", label);
    button.setAttribute("title", label);
    button.querySelector("[data-theme-icon='light']").hidden = !dark;
    button.querySelector("[data-theme-icon='dark']").hidden = dark;
  }

  function bind() {
    var button = document.getElementById("theme-toggle");
    if (!button || !window.__gogoTheme) {
      return;
    }
    paint(button);
    button.addEventListener("click", function () {
      window.__gogoTheme.toggle();
      paint(button);
    });
    // The preference is shared across the shell's tabs, so toggling in one tab
    // updates the other without a reload.
    window.addEventListener("storage", function (event) {
      if (event.key === "gogo-theme" && event.newValue) {
        window.__gogoTheme.set(event.newValue);
        paint(button);
      }
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", bind);
  } else {
    bind();
  }
})();
`

// FormGuardJS covers the two things htmx gives an htmx-driven form for free and
// a plain POST form nothing gives it for.
//
//  1. Confirmation for irreversible actions. The seller console deletes a
//     product, a category and a supplier, and voids a paid transaction, on a
//     single tap of a button whose label is the only warning. Each of those
//     buttons now carries data-confirm; this turns that into a window.confirm.
//     It is delegated from document and driven off a data attribute rather than
//     an inline handler, so a reader whose JavaScript did not load still gets
//     a working form — the confirmation is an enhancement, not a gate.
//
//  2. One submission at a time. A double-tapped "Bayar" charged a customer
//     twice; a double-tapped "Simpan" wrote two rows. The form is marked in
//     flight on submit and later submits are swallowed. The button is NOT
//     disabled: disabling the default submit control makes implicit submission
//     (pressing Enter in a text field) stop working, which would break the form
//     for keyboard users. Pointer events are suppressed instead, and the
//     keyboard path is covered by the form-level guard.
//
// htmx swaps do not navigate, so a form's in-flight mark has to be cleared when
// the request settles — otherwise an htmx-driven form would stay wedged.
const FormGuardJS = `
(function () {
  function onReady(fn) {
    if (document.readyState === "loading") {
      document.addEventListener("DOMContentLoaded", fn);
    } else {
      fn();
    }
  }

  function confirmFor(form) {
    var button = form.querySelector("[data-confirm]");
    return button ? button.getAttribute("data-confirm") : "";
  }

  function setPending(form, pending) {
    if (pending) {
      form.setAttribute("data-in-flight", "true");
    } else {
      form.removeAttribute("data-in-flight");
    }
    form.querySelectorAll("[data-submitting]").forEach(function (button) {
      button.setAttribute("aria-busy", String(pending));
      button.classList.toggle("btn-pending", pending);
    });
  }

  onReady(function () {
    document.addEventListener("submit", function (event) {
      var form = event.target;
      if (!form || form.tagName !== "FORM") {
        return;
      }
      if (form.hasAttribute("data-in-flight")) {
        event.preventDefault();
        return;
      }
      var message = confirmFor(form);
      if (message && !window.confirm(message)) {
        event.preventDefault();
        return;
      }
      setPending(form, true);
    }, true);

    document.body.addEventListener("htmx:afterRequest", function (event) {
      var form = event.detail && event.detail.elt && event.detail.elt.closest("form");
      if (form) {
        setPending(form, false);
      }
    });

    // Restoring from the back/forward cache replays neither submit nor a
    // navigation, so a page can come back with its buttons still suppressed.
    window.addEventListener("pageshow", function (event) {
      if (!event.persisted) {
        return;
      }
      document.querySelectorAll('form[data-in-flight]').forEach(function (form) {
        setPending(form, false);
      });
    });
  });
})();
`

// DrawerTriggerJS makes the sidebar drawer's toggle operable by keyboard. The
// trigger is a <label> so a click natively toggles the drawer checkbox, but a
// label is not focusable and does not activate on Enter/Space — so a keyboard
// user could not open the mobile sidebar at all. ui.DrawerTrigger gives it
// tabindex + role="button"; this forwards the two keys a button would honour to
// the click the label already handles.
const DrawerTriggerJS = `
(function () {
  function bind(label) {
    label.addEventListener("keydown", function (event) {
      if (event.key === "Enter" || event.key === " " || event.key === "Spacebar") {
        event.preventDefault();
        label.click();
      }
    });
  }
  function init() {
    document.querySelectorAll("label[role='button'][for]").forEach(bind);
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
`

// HtmxConfigJS configures the shell's htmx instance. Two things matter here.
//
//  1. htmx silently drops a 4xx/5xx response: it does not swap, and it swallows
//     the error, so a failed delete looks like a dead button. The app answers
//     *validation* failures with 200 + an out-of-band alert (htmx never swaps
//     error statuses — htmx.org/quirks), which means a real 4xx/5xx reaching this
//     handler is always an unexpected failure. Announcing it in the shell's
//     status region is what turns silence into a message.
//
//  2. includeIndicatorStyles is turned off because the stylesheet authors
//     `.htmx-indicator` itself; letting htmx inject its own copy would duplicate
//     the rule at runtime.
const HtmxConfigJS = `
(function () {
  if (!window.htmx) {
    return;
  }
  window.htmx.config.defaultSettleDelay = 0;
  window.htmx.config.includeIndicatorStyles = false;
  window.htmx.config.selfRequestsOnly = true;

  function announce(message) {
    var region = document.getElementById("page-status");
    if (!region) {
      return;
    }
    region.textContent = message;
  }

  function onReady(fn) {
    if (document.readyState === "loading") {
      document.addEventListener("DOMContentLoaded", fn);
    } else {
      fn();
    }
  }

  onReady(function () {
    document.body.addEventListener("htmx:responseError", function (event) {
      var status = event.detail.xhr.status;
      announce(
        status === 0
          ? "Could not reach the server. Check your connection and try again."
          : "That request failed (" + status + "). Please try again."
      );
    });
    document.body.addEventListener("htmx:sendError", function () {
      announce("Could not reach the server. Check your connection and try again.");
    });
    document.body.addEventListener("htmx:timeout", function () {
      announce("That request took too long. Please try again.");
    });
    // A successful mutation clears whatever the last failure announced.
    document.body.addEventListener("htmx:afterSwap", function () {
      announce("");
    });
  });
})();
`
