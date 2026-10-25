// Za Wiper: one shared view.  The wall and the bay pages listen to the same
// frames from /events, and poll /api/frame every 2 s while the stream is
// down.  A frame updates a running job's map and numbers in place, and asks
// for fresh HTML only when a bay's signature changes.
(() => {
  // The DEFRAG glyph for each cell byte.  web/surface.go holds the same
  // table; keep the two in step.
  const GLYPHS = {
    ".": ["░", "todo"], W: ["W", "cur"], R: ["r", "cur"],
    w: ["■", "written"], v: ["■", "verified"], s: ["■", "slow"], B: ["B", "bad"],
  };

  function cells(el, map) {
    if (el.dataset.drawn === map) return;
    const frag = document.createDocumentFragment();
    let span = null, cls = "";
    for (const ch of map) {
      const [glyph, c] = GLYPHS[ch] || ["?", "unk"];
      if (c !== cls) {
        span = document.createElement("span");
        span.className = "c-" + c;
        frag.appendChild(span);
        cls = c;
      }
      span.textContent += glyph;
    }
    el.replaceChildren(frag);
    el.dataset.drawn = map;
  }

  // The throughput graph: TraceRows in web/surface.go draws the same rows
  // from the same string; keep the two in step.
  const ROWS = 6;
  function trace(el, enc) {
    if (el.dataset.drawn === enc) return;
    const frag = document.createDocumentFragment();
    for (let row = ROWS - 1; row >= 0; row--) {
      let span = null, cls = "";
      for (let c = 0; c + 1 < enc.length; c += 2) {
        let lv = enc[c + 1], k = "t-r";
        if (lv === "-") { lv = enc[c]; k = "t-w"; }
        const h = lv.charCodeAt(0) - 48;
        const g = h >= 2 * row + 2 ? "█" : h === 2 * row + 1 ? "▄" : " ";
        if (k !== cls) {
          span = document.createElement("span");
          span.className = k;
          frag.appendChild(span);
          cls = k;
        }
        span.textContent += g;
      }
      if (row > 0) frag.appendChild(document.createTextNode("\n"));
    }
    el.replaceChildren(frag);
    el.dataset.drawn = enc;
  }

  const regions = [...document.querySelectorAll("[data-fragment]")];
  const say = document.getElementById("say");
  const notice = document.getElementById("notice");

  async function reload(region) {
    if (region.loading) { region.again = true; return; }
    region.loading = true;
    try {
      const r = await fetch(region.dataset.fragment, { cache: "no-store" });
      if (!r.ok) return;
      const html = await r.text();
      const open = region.querySelector("dialog[open]");
      const was = region.querySelector("[data-state]");
      if (open) open.close();
      region.innerHTML = html;
      if (open && notice) {
        notice.textContent = "The drive's facts changed while the confirm was open, so it closed.  Look again before you erase anything.";
        notice.hidden = false;
      }
      const now = region.querySelector("[data-state]");
      if (say && now && (!was || was.dataset.state !== now.dataset.state)) {
        say.textContent = now.dataset.key + " is now " + now.dataset.state + ".";
      }
    } catch (_) {
      // The next frame will try again.
    } finally {
      region.loading = false;
      if (region.again) { region.again = false; reload(region); }
    }
  }

  function apply(frame) {
    const byKey = new Map(frame.bays.map((b) => [b.key, b]));
    for (const region of regions) {
      const shown = region.querySelectorAll("[data-key]");
      let stale = "everyBay" in region.dataset && shown.length !== frame.bays.length;
      shown.forEach((el) => {
        const b = byKey.get(el.dataset.key);
        if (!b || b.sig !== el.dataset.sig) { stale = true; return; }
        if (!b.live) return;
        el.querySelectorAll("[data-field]").forEach((f) => {
          const v = b.live[f.dataset.field];
          if (typeof v !== "string") return;
          if ("cells" in f.dataset) cells(f, v);
          else if ("trace" in f.dataset) trace(f, v);
          else if (f.textContent !== v) f.textContent = v;
        });
      });
      if (stale) reload(region);
    }
  }

  let poll = null;
  async function pollOnce() {
    if (document.visibilityState !== "visible") return;
    try {
      const r = await fetch("/api/frame", { cache: "no-store" });
      if (r.ok) apply(await r.json());
    } catch (_) {
      // The next tick will try again.
    }
  }
  function startPoll() { if (!poll) poll = setInterval(pollOnce, 2000); }
  function stopPoll() { clearInterval(poll); poll = null; }

  if (regions.length) {
    if ("EventSource" in window) {
      const es = new EventSource("/events");
      es.addEventListener("frame", (e) => { stopPoll(); apply(JSON.parse(e.data)); });
      // EventSource reconnects by itself; poll until it does.
      es.addEventListener("error", startPoll);
    } else {
      startPoll();
    }
  }

  async function post(url, body) {
    const r = await fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Za-Wiper": "1" },
      body: JSON.stringify(body || {}),
    });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
  }

  function show(el, msg) {
    if (!el) return;
    el.textContent = msg;
    el.hidden = !msg;
  }

  document.addEventListener("click", async (e) => {
    if (e.target.closest("[data-print]")) {
      window.print();
      return;
    }
    const region = e.target.closest("[data-fragment]");

    const opener = e.target.closest("[data-open-confirm]");
    if (opener && region) {
      const dlg = region.querySelector("dialog.confirm");
      if (dlg && !opener.disabled) {
        if (notice) notice.hidden = true;
        show(dlg.querySelector("[data-confirm-error]"), "");
        dlg.showModal();
      }
      return;
    }

    // The confirm posts what it was drawn from: this serial, this insertion.
    const go = e.target.closest("[data-confirm]");
    if (go) {
      const dlg = go.closest("dialog");
      go.disabled = true;
      try {
        await post(dlg.dataset.url, { serial: dlg.dataset.serial, session: dlg.dataset.session });
        dlg.close();
        if (region) reload(region);
      } catch (err) {
        show(dlg.querySelector("[data-confirm-error]"), err.message);
        go.disabled = false;
      }
      return;
    }

    const btn = e.target.closest("[data-act]");
    if (!btn) return;
    const label = btn.textContent;
    btn.disabled = true;
    if (btn.dataset.busy) btn.textContent = btn.dataset.busy;
    try {
      await post(btn.dataset.act);
      if (region) setTimeout(() => reload(region), 300);
    } catch (err) {
      show(region && region.querySelector("[data-error]"), err.message);
      btn.disabled = false;
      btn.textContent = label;
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
