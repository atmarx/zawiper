// Za Wiper's arcade: games played with a surface map's own blocks.  F10 on
// a page with a map starts one, and so does a click on the map's title bar.
// Esc stops it.
//
// It's for whoever is watching, and nobody else.  It never talks to the hub
// or the bay.  The live map goes on updating underneath, hidden, and comes
// back the moment the game ends, as current as if nothing had happened.
(() => {
  // The glyphs zawiper.js draws, by the class it gives them.  A brick is a
  // cell: a ▒ (slow) takes two hits and mends to ■ on the first, a B (bad)
  // doesn't break, and the blinking block under the head is worth most.
  const SCORE = { todo: 10, written: 10, verified: 10, slow: 20, cur: 50 };

  function colors() {
    const css = getComputedStyle(document.documentElement);
    const v = (n, d) => css.getPropertyValue(n).trim() || d;
    return {
      field: v("--cga-blue", "#0000aa"),
      todo: [v("--cga-gray", "#aaaaaa")],
      written: [v("--cga-lcyan", "#55ffff")],
      verified: [v("--cga-white", "#ffffff")],
      slow: [v("--cga-lmagenta", "#ff55ff")],
      cur: ["#000", v("--cga-yellow", "#ffff55")],
      bad: [v("--cga-white", "#ffffff"), v("--cga-red", "#aa0000")],
      paddle: v("--cga-white", "#ffffff"),
      ball: v("--cga-yellow", "#ffff55"),
      box: v("--cga-gray", "#aaaaaa"),
    };
  }

  // The bricks, read off the map as it's drawn now: one per character, in
  // the order the browser wraps them.
  function bricks(map, cols) {
    const out = [];
    let i = 0;
    for (const span of map.querySelectorAll("span")) {
      const cls = (span.className.match(/c-(\w+)/) || [])[1] || "todo";
      for (const glyph of span.textContent) {
        out.push({ c: i % cols, r: Math.floor(i / cols), glyph, cls, hits: cls === "slow" ? 2 : 1 });
        i++;
      }
    }
    return out;
  }

  // Breakout, with the blocks marching down a row at a time, sooner each
  // time, until they reach the paddle or the paddle misses the ball.
  function breakout(map, done) {
    const font = getComputedStyle(map).fontFamily;
    const probe = document.createElement("span");
    probe.textContent = "█";
    map.appendChild(probe);
    const box = probe.getBoundingClientRect();
    probe.remove();
    const st = getComputedStyle(map);
    const cw = Math.max(4, Math.round(box.width)), ch = Math.round(parseFloat(st.lineHeight)) || 16;
    const inner = map.clientWidth - parseFloat(st.paddingLeft) - parseFloat(st.paddingRight);
    const cols = Math.max(20, Math.floor(inner / cw));
    const all = bricks(map, cols);
    if (!all.length) return null;
    const brickRows = all[all.length - 1].r + 1;
    const rows = brickRows + Math.max(12, Math.ceil(brickRows * 0.8));
    const W = cols * cw, H = rows * ch;
    const pal = colors();

    const stage = document.createElement("div");
    stage.className = "dos-map dos-arcade";
    const hud = document.createElement("div");
    hud.className = "dos-hud";
    const canvas = document.createElement("canvas");
    const dpr = window.devicePixelRatio || 1;
    canvas.width = W * dpr;
    canvas.height = H * dpr;
    canvas.style.width = W + "px";
    canvas.style.height = H + "px";
    canvas.setAttribute("role", "img");
    canvas.setAttribute("aria-label", "Breakout, played with the surface map's blocks.  Escape returns to the map.");
    stage.append(hud, canvas);
    map.before(stage);
    map.hidden = true;
    const ctx = canvas.getContext("2d");
    ctx.scale(dpr, dpr);
    ctx.imageSmoothingEnabled = false;
    ctx.font = `${ch}px ${font}`;
    ctx.textBaseline = "top";

    const grid = new Map(all.map((b) => [b.r * cols + b.c, b]));
    let breakable = all.filter((b) => b.cls !== "bad").length;
    let offset = 0, marchEvery = 14, marchIn = marchEvery;
    let score = 0, speed = cw * 22, over = null, overAt = 0;
    const paddle = { w: cw * 9, x: W / 2, y: (rows - 1) * ch };
    const ball = { x: W / 2, y: paddle.y - ch / 2, vx: 0, vy: 0, stuck: true };
    const keys = { left: false, right: false };
    const bits = []; // pieces of broken blocks, falling away

    function launch() {
      if (!ball.stuck || over) return;
      ball.stuck = false;
      const a = (Math.random() - 0.5) * 0.9;
      ball.vx = speed * Math.sin(a);
      ball.vy = -speed * Math.cos(a);
    }

    function at(px, py) {
      if (px < 0 || px >= W) return null;
      const r = Math.floor(py / ch) - offset, c = Math.floor(px / cw);
      return r < 0 ? null : grid.get(r * cols + c) || null;
    }

    function hit(b) {
      if (b.cls === "bad") return;
      b.hits--;
      if (b.hits > 0) {
        b.cls = "verified";
        b.glyph = "■";
        score += 5;
        return;
      }
      grid.delete(b.r * cols + b.c);
      breakable--;
      score += SCORE[b.cls] || 10;
      speed = Math.min(cw * 44, speed * 1.006);
      bits.push({ x: b.c * cw, y: (b.r + offset) * ch, vx: (Math.random() - 0.5) * cw * 8, vy: -ch * 3, life: 0.7, glyph: b.glyph, cls: b.cls });
      if (breakable === 0) end("SURFACE CLEAR");
    }

    function end(why) {
      if (over) return;
      over = why;
      overAt = performance.now();
    }

    function step(dt) {
      const pv = cw * 40;
      if (keys.left) paddle.x -= pv * dt;
      if (keys.right) paddle.x += pv * dt;
      paddle.x = Math.min(W - paddle.w / 2, Math.max(paddle.w / 2, paddle.x));

      marchIn -= dt;
      if (marchIn <= 0) {
        offset++;
        marchEvery = Math.max(3, marchEvery * 0.88);
        marchIn = marchEvery;
        for (const b of grid.values()) {
          if (b.cls !== "bad" && b.r + offset >= rows - 2) return end("THE BLOCKS GOT THROUGH");
        }
      }

      for (const p of bits) {
        p.vy += ch * 12 * dt;
        p.x += p.vx * dt;
        p.y += p.vy * dt;
        p.life -= dt;
      }
      while (bits.length && bits[0].life <= 0) bits.shift();

      if (ball.stuck) {
        ball.x = paddle.x;
        ball.y = paddle.y - ch / 2;
        return;
      }
      // Small steps, so a fast ball can't pass through a block.
      const n = Math.ceil((Math.abs(ball.vx) + Math.abs(ball.vy)) * dt / (cw / 2));
      for (let i = 0; i < n && !over; i++) {
        const dx = ball.vx * dt / n, dy = ball.vy * dt / n;
        ball.x += dx;
        if (ball.x < 0 || ball.x >= W) { ball.x -= dx; ball.vx = -ball.vx; }
        else {
          const b = at(ball.x, ball.y);
          if (b) { hit(b); ball.x -= dx; ball.vx = -ball.vx; }
        }
        ball.y += dy;
        if (ball.y < 0) { ball.y -= dy; ball.vy = -ball.vy; continue; }
        const b = at(ball.x, ball.y);
        if (b) { hit(b); ball.y -= dy; ball.vy = -ball.vy; continue; }
        if (ball.vy > 0 && ball.y >= paddle.y && ball.y < paddle.y + ch && Math.abs(ball.x - paddle.x) <= paddle.w / 2 + cw / 2) {
          // Where it lands on the paddle sets the angle, up to 60° off.
          const a = ((ball.x - paddle.x) / (paddle.w / 2)) * (Math.PI / 3);
          ball.vx = speed * Math.sin(a);
          ball.vy = -speed * Math.cos(a);
          ball.y = paddle.y - 1;
        } else if (ball.y >= H) {
          end("GAME OVER");
        }
      }
    }

    function cell(glyph, x, y, cls) {
      const [fg, bg] = pal[cls] || pal.todo;
      if (bg) { ctx.fillStyle = bg; ctx.fillRect(x, y, cw, ch); }
      ctx.fillStyle = fg;
      ctx.fillText(glyph, x, y);
    }

    function draw() {
      ctx.fillStyle = pal.field;
      ctx.fillRect(0, 0, W, H);
      for (const b of grid.values()) cell(b.glyph, b.c * cw, (b.r + offset) * ch, b.cls);
      for (const p of bits) {
        ctx.globalAlpha = Math.max(0, p.life / 0.7);
        cell(p.glyph, Math.round(p.x), Math.round(p.y), p.cls);
      }
      ctx.globalAlpha = 1;
      ctx.fillStyle = pal.paddle;
      ctx.fillText("▀".repeat(Math.round(paddle.w / cw)), Math.round(paddle.x - paddle.w / 2), paddle.y);
      ctx.fillStyle = pal.ball;
      ctx.fillText("☻", Math.round(ball.x - cw / 2), Math.round(ball.y - ch / 2));
      if (over) {
        const msg = ` ${over} · ${score} `;
        const x = Math.max(0, Math.floor((cols - msg.length) / 2)) * cw, y = Math.floor(rows / 2) * ch;
        ctx.fillStyle = pal.box;
        ctx.fillRect(x - cw, y - ch, (msg.length + 2) * cw, ch * 3);
        ctx.fillStyle = "#000";
        ctx.fillText(msg, x, y);
      }
      hud.textContent = `SCORE ${score}   ←/→ or the mouse · Space launches · Esc goes back to the map`;
    }

    function onKey(e, down) {
      if (e.key === "ArrowLeft" || e.key === "a") keys.left = down;
      else if (e.key === "ArrowRight" || e.key === "d") keys.right = down;
      else if (e.key === " " && down) launch();
      else if (e.key === "Escape" && down) return stop(), e.preventDefault();
      else return;
      e.preventDefault();
    }
    const kd = (e) => onKey(e, true), ku = (e) => onKey(e, false);
    const aim = (x) => { paddle.x = x - canvas.getBoundingClientRect().left; };
    const mm = (e) => aim(e.clientX);
    const tm = (e) => { aim(e.touches[0].clientX); e.preventDefault(); };
    window.addEventListener("keydown", kd);
    window.addEventListener("keyup", ku);
    canvas.addEventListener("mousemove", mm);
    canvas.addEventListener("touchmove", tm, { passive: false });
    canvas.addEventListener("touchstart", launch);
    canvas.addEventListener("click", launch);

    let then = performance.now(), raf = 0, stopped = false;
    function stop() {
      if (stopped) return;
      stopped = true;
      cancelAnimationFrame(raf);
      window.removeEventListener("keydown", kd);
      window.removeEventListener("keyup", ku);
      stage.remove();
      map.hidden = false;
      done();
    }
    function frame(now) {
      // A fresh fragment from the hub replaces the map and the game with
      // it: nothing to put back.
      if (!stage.isConnected) return stop();
      const dt = Math.min(1 / 30, (now - then) / 1000);
      then = now;
      if (!over) step(dt);
      draw();
      if (over && now - overAt > 2500) return stop();
      raf = requestAnimationFrame(frame);
    }
    raf = requestAnimationFrame(frame);
    return stop;
  }

  // The cabaret's bill.  Breakout opens; more can follow it.
  const GAMES = [breakout];
  let playing = null;

  function play(map) {
    if (playing || !map || map.hidden) return;
    const go = () => {
      playing = GAMES[0](map, () => { playing = null; });
    };
    // Draw in the VGA font, not whatever stood in while it loaded.
    if (document.fonts && document.fonts.load) {
      document.fonts.load(`16px ${getComputedStyle(map).fontFamily}`).then(go, go);
    } else {
      go();
    }
  }

  window.addEventListener("keydown", (e) => {
    if (e.key !== "F10" || playing || document.querySelector("dialog[open]")) return;
    const map = document.querySelector(".dos-map[data-cells]");
    if (!map) return;
    e.preventDefault();
    play(map);
  });
  document.addEventListener("click", (e) => {
    const bar = e.target.closest(".dos-bar");
    if (bar && !playing) play(bar.closest(".dos").querySelector(".dos-map[data-cells]"));
  });
})();
