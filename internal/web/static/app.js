// Docker Hub Image Statistics UI. Vanilla JS, no external dependencies.
// Every value is rendered with textContent (never innerHTML) to avoid XSS.
"use strict";

(() => {
  const THEME_KEY = "dhis-theme";
  const DOCKER_HUB = "https://hub.docker.com";
  const SVG_NS = "http://www.w3.org/2000/svg";
  const STATUS_POLL_MS = 60_000;

  const $ = (sel) => document.querySelector(sel);
  const $$ = (sel) => Array.from(document.querySelectorAll(sel));

  const fmtFull = new Intl.NumberFormat();
  const fmtCompact = new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 });
  const fmtDate = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });
  const fmtHour = new Intl.DateTimeFormat(undefined, { month: "short", day: "2-digit", hour: "2-digit", minute: "2-digit" });

  const state = {
    sort: "pulls",
    query: "",
    images: [],
    selected: null,
    range: "24h",
    nextRun: null,
    refreshedAfterNextRun: false,
  };

  // ---------- Theme ----------
  function applyTheme(theme) {
    document.documentElement.setAttribute("data-theme", theme);
    $$("[data-theme-choice]").forEach((b) => b.setAttribute("aria-checked", String(b.dataset.themeChoice === theme)));
    try { localStorage.setItem(THEME_KEY, theme); } catch (e) { /* ignore */ }
  }

  // ---------- Helpers ----------
  async function getJSON(path) {
    const res = await fetch(path, { headers: { Accept: "application/json" }, cache: "no-store" });
    if (!res.ok) throw new Error(`${path}: HTTP ${res.status}`);
    return res.json();
  }

  function el(tag, opts = {}, ...children) {
    const node = document.createElement(tag);
    if (opts.className) node.className = opts.className;
    if (opts.text !== undefined) node.textContent = opts.text;
    if (opts.title) node.title = opts.title;
    for (const c of children) node.append(c);
    return node;
  }

  function parseTime(v) {
    if (!v) return null;
    const d = new Date(v);
    return Number.isNaN(d.getTime()) || d.getFullYear() < 2000 ? null : d;
  }

  function formatDate(v) {
    const d = parseTime(v);
    return d ? fmtDate.format(d) : "—";
  }

  function formatCountdown(ms) {
    if (ms <= 0) return "(in progress)";
    const s = Math.floor(ms / 1000);
    const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
    return h > 0 ? `(in ${h}h ${m}m)` : `(in ${m}m ${String(sec).padStart(2, "0")}s)`;
  }

  function formatInterval(goDuration) {
    if (!goDuration) return "—";
    // Go durations such as "1h0m0s" -> "1h", "30m0s" -> "30m".
    return goDuration.replace(/(\D)0s$/, "$1").replace(/(\D)0m$/, "$1") || goDuration;
  }

  function snackbar(message) {
    const bar = $("#snackbar");
    bar.textContent = message;
    bar.hidden = false;
    clearTimeout(snackbar.timer);
    snackbar.timer = setTimeout(() => { bar.hidden = true; }, 5000);
  }

  function hubURL(img) {
    const ns = encodeURIComponent(img.namespace), name = encodeURIComponent(img.name);
    return img.namespace === "library" ? `${DOCKER_HUB}/_/${name}` : `${DOCKER_HUB}/r/${ns}/${name}`;
  }

  function windowCell(img, w) {
    const wp = (img.pulls && img.pulls[w]) || { pulls: 0, complete: false };
    const td = el("td", { className: "num", text: fmtFull.format(wp.pulls) });
    if (!wp.complete) {
      td.append(el("span", { className: "partial", text: "*", title: "Window still filling (lower bound)" }));
    }
    return td;
  }

  function lastCollectionCell(img) {
    const lc = img.last_collection || { pulls: 0 };
    const td = el("td", { className: "num", text: fmtFull.format(lc.pulls || 0) });
    td.title = lc.previous_at
      ? `Pulls between ${formatDate(lc.previous_at)} and ${formatDate(img.collected_at)}`
      : "First collection: no previous snapshot yet";
    return td;
  }

  // ---------- Summary + schedule ----------
  function renderSummary(data) {
    const s = data.summary, st = data.status, v = data.version;
    const setNum = (id, n) => {
      const node = $(id);
      node.textContent = fmtCompact.format(n || 0);
      node.title = fmtFull.format(n || 0);
    };
    setNum("#sum-total", s.pulls_total);
    setNum("#sum-last", s.pulls_last_collection);
    setNum("#sum-1d", s.pulls["1d"]);
    setNum("#sum-7d", s.pulls["7d"]);
    setNum("#sum-30d", s.pulls["30d"]);
    setNum("#sum-images", s.images);
    setNum("#sum-stars", s.stars);

    $("#version").textContent = `v${v.version}`;
    $("#last-update").textContent = formatDate(st.last_success);
    $("#interval").textContent = formatInterval(st.interval);

    const next = parseTime(st.next_run);
    if (!state.nextRun || (next && next.getTime() !== state.nextRun.getTime())) state.refreshedAfterNextRun = false;
    state.nextRun = next;
    $("#next-update").textContent = st.collecting ? "collecting now…" : formatDate(st.next_run);
    tickCountdown();

    const warn = $("#collect-warning");
    const warnings = st.warnings || [];
    warn.hidden = warnings.length === 0;
    warn.textContent = warnings.join(" ");

    const err = $("#collect-error");
    err.hidden = !st.last_error;
    err.textContent = st.last_error ? `Last collection had errors: ${st.last_error}` : "";
  }

  function tickCountdown() {
    const node = $("#next-countdown");
    if (!state.nextRun) { node.textContent = ""; return; }
    const remaining = state.nextRun.getTime() - Date.now();
    node.textContent = formatCountdown(remaining);
    // Shortly after the scheduled run, reload once to show the new data.
    if (remaining < -15_000 && !state.refreshedAfterNextRun) {
      state.refreshedAfterNextRun = true;
      refreshAll();
    }
  }

  // ---------- Images table ----------
  function renderImages() {
    const tbody = $("#images");
    tbody.replaceChildren();
    $("#empty").hidden = state.images.length > 0;
    for (const img of state.images) {
      const key = `${img.namespace}/${img.name}`;
      const nameCell = el("td", {},
        el("span", { className: "ns", text: `${img.namespace}/` }),
        el("span", { className: "name", text: img.name }));
      if (img.description) nameCell.title = img.description;
      const row = el("tr", {},
        nameCell,
        el("td", { className: "num", text: fmtFull.format(img.pulls_total) }),
        lastCollectionCell(img),
        windowCell(img, "1d"),
        windowCell(img, "7d"),
        windowCell(img, "30d"),
        el("td", { className: "num", text: fmtFull.format(img.stars) }),
        el("td", { className: "num", text: formatDate(img.last_updated) }));
      row.tabIndex = 0;
      row.dataset.key = key;
      if (state.selected === key) row.classList.add("selected");
      row.addEventListener("click", () => selectImage(img));
      row.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); selectImage(img); } });
      tbody.append(row);
    }
  }

  async function loadImages() {
    const params = new URLSearchParams({ sort: state.sort });
    if (state.query) params.set("q", state.query);
    const data = await getJSON(`api/v1/images?${params}`);
    state.images = data.images || [];
    renderImages();
  }

  async function loadSummary() {
    renderSummary(await getJSON("api/v1/summary"));
  }

  async function refreshAll() {
    try {
      await Promise.all([loadSummary(), loadImages()]);
      if (state.selected) await loadHistory();
    } catch (e) {
      snackbar(`Could not load data: ${e.message}`);
    }
  }

  // ---------- Detail: hourly statistics ----------
  function selectImage(img) {
    state.selected = `${img.namespace}/${img.name}`;
    $$("#images tr").forEach((r) => r.classList.toggle("selected", r.dataset.key === state.selected));
    $("#detail").hidden = false;
    $("#detail-title").textContent = state.selected;
    $("#detail-desc").textContent = img.description || "";
    const lc = img.last_collection || { pulls: 0 };
    $("#detail-last").textContent = lc.previous_at
      ? `Last collection: ${fmtFull.format(lc.pulls)} pulls (${formatDate(lc.previous_at)} → ${formatDate(img.collected_at)})`
      : "Last collection: first snapshot, no previous collection to compare yet";
    $("#detail-link").href = hubURL(img);
    loadHistory().catch((e) => snackbar(`Could not load history: ${e.message}`));
    $("#detail").scrollIntoView({ behavior: "smooth", block: "start" });
  }

  async function loadHistory() {
    const [ns, name] = state.selected.split("/");
    const data = await getJSON(
      `api/v1/images/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/history?range=${state.range}&step=1h`);
    renderChart(data.points || []);
    renderHourly(data.points || []);
  }

  function renderChart(points) {
    const box = $("#chart");
    box.replaceChildren();
    const W = 960, H = 240, PAD_L = 56, PAD_B = 28, PAD_T = 12, PAD_R = 8;
    const svg = document.createElementNS(SVG_NS, "svg");
    svg.setAttribute("viewBox", `0 0 ${W} ${H}`);
    svg.setAttribute("preserveAspectRatio", "none");
    const add = (tag, attrs, text) => {
      const n = document.createElementNS(SVG_NS, tag);
      for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, String(v));
      if (text !== undefined) n.textContent = text;
      svg.append(n);
      return n;
    };
    const series = points.slice(1); // the first point has no previous baseline
    if (series.length === 0) {
      add("text", { x: W / 2, y: H / 2, "text-anchor": "middle", class: "no-data" },
        "Not enough history yet: hourly values appear after the second collection.");
      box.append(svg);
      return;
    }
    const maxV = Math.max(1, ...series.map((p) => p.pulls));
    const plotW = W - PAD_L - PAD_R, plotH = H - PAD_T - PAD_B;
    for (let i = 0; i <= 4; i++) {
      const y = PAD_T + plotH - (plotH * i) / 4;
      add("line", { x1: PAD_L, x2: W - PAD_R, y1: y, y2: y, class: i === 0 ? "axis" : "grid" });
      add("text", { x: PAD_L - 8, y: y + 4, "text-anchor": "end" }, fmtCompact.format((maxV * i) / 4));
    }
    const slot = plotW / series.length;
    const barW = Math.max(1, slot * 0.7);
    const labelEvery = Math.ceil(series.length / 8);
    series.forEach((p, i) => {
      const h = (p.pulls / maxV) * plotH;
      const x = PAD_L + i * slot + (slot - barW) / 2;
      const bar = add("rect", { x, y: PAD_T + plotH - h, width: barW, height: Math.max(h, 0.5), rx: Math.min(2, barW / 2), class: "bar" });
      const t = document.createElementNS(SVG_NS, "title");
      t.textContent = `${fmtHour.format(new Date(p.time))}: ${fmtFull.format(p.pulls)} pulls`;
      bar.append(t);
      if (i % labelEvery === 0) {
        add("text", { x: x + barW / 2, y: H - 8, "text-anchor": "middle" }, fmtHour.format(new Date(p.time)));
      }
    });
    box.setAttribute("aria-label", `Pulls per hour, ${series.length} hours, maximum ${fmtFull.format(maxV)}`);
    box.append(svg);
  }

  function renderHourly(points) {
    const tbody = $("#hourly");
    tbody.replaceChildren();
    for (let i = points.length - 1; i >= 1; i--) {
      const p = points[i];
      tbody.append(el("tr", {},
        el("td", { text: fmtHour.format(new Date(p.time)) }),
        el("td", { className: "num", text: fmtFull.format(p.pulls) }),
        el("td", { className: "num", text: fmtFull.format(p.pulls_total) })));
    }
  }

  // ---------- Wiring ----------
  function init() {
    applyTheme(document.documentElement.getAttribute("data-theme") === "light" ? "light" : "dark");
    $$("[data-theme-choice]").forEach((b) => b.addEventListener("click", () => applyTheme(b.dataset.themeChoice)));

    $$(".sort").forEach((b) => b.addEventListener("click", () => {
      state.sort = b.dataset.sort;
      $$(".sort").forEach((x) => x.classList.toggle("active", x === b));
      loadImages().catch((e) => snackbar(e.message));
    }));

    let debounce;
    $("#search").addEventListener("input", (e) => {
      clearTimeout(debounce);
      debounce = setTimeout(() => {
        state.query = e.target.value.trim();
        loadImages().catch((err) => snackbar(err.message));
      }, 250);
    });

    $$("[data-range]").forEach((b) => b.addEventListener("click", () => {
      state.range = b.dataset.range;
      $$("[data-range]").forEach((x) => x.setAttribute("aria-checked", String(x === b)));
      $("#chart-title").textContent = "Pulls per hour";
      loadHistory().catch((e) => snackbar(e.message));
    }));

    $("#detail-close").addEventListener("click", () => {
      $("#detail").hidden = true;
      state.selected = null;
      $$("#images tr").forEach((r) => r.classList.remove("selected"));
    });

    refreshAll();
    setInterval(tickCountdown, 1000);
    setInterval(() => loadSummary().catch(() => {}), STATUS_POLL_MS);
  }

  document.addEventListener("DOMContentLoaded", init);
})();
