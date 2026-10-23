// Za Wiper: keep the bay card live, re-read on request, filter the list.
(() => {
  const bay = document.querySelector("[data-poll]");
  let last = bay ? bay.innerHTML : "";

  async function refresh() {
    if (!bay) return;
    try {
      const r = await fetch(bay.dataset.poll, { cache: "no-store" });
      const html = await r.text();
      if (html !== last) {
        bay.innerHTML = html;
        last = html;
      }
    } catch (_) {
      // The next tick will try again.
    }
  }
  if (bay) {
    setInterval(() => { if (document.visibilityState === "visible") refresh(); }, 2000);
    document.addEventListener("visibilitychange", refresh);
  }

  document.addEventListener("click", async (e) => {
    if (e.target.closest("[data-print]")) {
      window.print();
      return;
    }
    const btn = e.target.closest("[data-reread]");
    if (!btn) return;
    btn.disabled = true;
    btn.textContent = "Re-reading…";
    try {
      await fetch("/api/bay/reread", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-Za-Wiper": "1" },
        body: "{}",
      });
    } finally {
      setTimeout(refresh, 300);
    }
  });

  const q = document.querySelector("[data-filter]");
  if (q) {
    const rows = document.querySelectorAll(q.dataset.filter + " tr");
    q.addEventListener("input", () => {
      const needle = q.value.trim().toLowerCase();
      rows.forEach((tr) => {
        tr.hidden = needle !== "" && !tr.dataset.search.toLowerCase().includes(needle);
      });
    });
  }
})();
