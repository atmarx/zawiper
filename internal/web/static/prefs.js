// The viewer's own layout, applied before the page draws so it doesn't
// jump.  Per browser, never sent anywhere: "wide" fills the window, and
// without it the map is DEFRAG's 80 columns.
try {
  if (localStorage.getItem("zw-wide") === "1") document.documentElement.classList.add("wide");
} catch (_) {
  // No storage (a private window, say): the narrow layout it is.
}
