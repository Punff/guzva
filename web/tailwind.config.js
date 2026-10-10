// Tailwind compile target for the guzva consoles and public page.
// Standalone CLI only — no node_modules, no npm, PLAN §2.
// The Makefile owns the exact invocation; this file carries only intent.
//
// v4 reads the theme from CSS (@theme in web/input.css), not from a JS
// file. This file exists only to pin the content globs in one place and to
// document the nb: tailwind.config is a JS file purely for editor tooling;
// the standalone CLI is driven by CSS + flags.
/** @type {import('tailwindcss').Config} */
module.exports = {
  // Observed member classes live in Go templates, not in JS.
  content: ["./internal/**/*.html", "./web/**/*.css"],
  theme: {},
};
