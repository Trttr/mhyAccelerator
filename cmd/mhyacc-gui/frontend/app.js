"use strict";

const I18N = {
  zh: {
    subtitle: "米哈游国服启动器下载加速",
    gamesTitle: "加速的游戏",
    start: "开始加速", stop: "停止加速", stopping: "正在停止…", preparing: "准备中…",
    speed: "当前速度", traffic: "已加速流量", uptime: "运行时间", launcher: "启动器", peak: "最高速度",
    chartTitle: "最近 5 分钟",
    eventsTitle: "事件记录",
    noEvents: "开始加速后，这里会显示节点测速、节点切换和启动器的状态变化。",
    footer: "非官方工具，与米哈游无关联。只转发连接，不解密流量，不修改任何游戏文件。",
    pillOff: "未加速", pillPrep: "准备中", pillOn: "加速中",
    idleHint: "选择游戏，然后开始加速",
    detecting: "检测中",
    phase: {
      discovering: "正在探测可用节点…",
      wait_launcher: "等待米哈游启动器退出…",
      running: "现在可以打开启动器下载了",
      optimizing: "正在后台优化节点",
      optimized: "节点已优化",
      stopping: "正在停止并还原…",
      stopped: "已停止，hosts 已还原",
    },
    launcherState: { started: "下载中", paused: "已暂停", finished: "已完成", "": "—" },
    proto: { "http/1.1": "HTTP/1.1 多连接", h2: "HTTP/2 单连接" },
    protocol: "协议", conns: "连接", rate: "速度", bytes: "已加速", switches: "节点切换", baseline: "基准速度(估)",
    node: "节点", score: "评分", state: "状态", inUse: "使用中", standby: "备用", resting: "冷却中", observing: "观察中", testing: "复测中",
    gainText: "比基准速度快 {d}（约 {x} 倍）", gainNone: "与基准速度相近", gainPending: "正在估算基准速度…", gainUnavailable: "无法估算基准速度",
    maintTitle: "数据与还原",
    maintNote: "每次停止加速或关闭本软件时，都会自动把 hosts 文件还原并删除开机清理任务；即使软件意外退出，保护程序也会负责还原。下面的按钮用于手动处理。",
    restoreHosts: "立即还原 hosts", clearData: "清除节点记录与设置",
    restored: "hosts 已还原", cleared: "已清除", busyMaint: "请先停止加速",
    confirmClear: "将清除节点历史记录、游戏选择和事件日志（保留语言设置）。下次加速时会重新探测节点。是否继续？",
    modalTitle: "请先退出米哈游启动器",
    modalBody: "检测到米哈游启动器正在运行。启动器会记住旧的下载节点，不退出的话加速不会生效。",
    step1: "在启动器里暂停下载",
    step2: "完全退出启动器（包括屏幕右下角托盘里的图标）",
    step3: "等加速开启后，再重新打开启动器继续下载",
    gotIt: "我知道了",
    waitingQuit: "正在等待启动器退出…",
    quitDone: "启动器已退出",
    selectOne: "请至少选择一个游戏",
  },
  en: {
    subtitle: "Download accelerator for the miHoYo CN launcher",
    gamesTitle: "Games",
    start: "Start", stop: "Stop", stopping: "Stopping…", preparing: "Preparing…",
    speed: "Speed", traffic: "Accelerated", uptime: "Running for", launcher: "Launcher", peak: "Peak",
    chartTitle: "Last 5 minutes",
    eventsTitle: "Event log",
    noEvents: "Once started, node tests, node switches and launcher state changes appear here.",
    footer: "Unofficial; not affiliated with miHoYo. Only relays connections — no decryption, no game files changed.",
    pillOff: "Off", pillPrep: "Preparing", pillOn: "Accelerating",
    idleHint: "Pick games, then start",
    detecting: "Detecting",
    phase: {
      discovering: "Probing available nodes…",
      wait_launcher: "Waiting for the miHoYo launcher to quit…",
      running: "You can open the launcher and download now",
      optimizing: "Optimizing nodes in the background",
      optimized: "Nodes optimized",
      stopping: "Stopping and restoring…",
      stopped: "Stopped; hosts restored",
    },
    launcherState: { started: "Downloading", paused: "Paused", finished: "Finished", "": "—" },
    proto: { "http/1.1": "HTTP/1.1 multi-connection", h2: "HTTP/2 single connection" },
    protocol: "Protocol", conns: "Conns", rate: "Speed", bytes: "Accelerated", switches: "Switches", baseline: "Baseline (est.)",
    node: "Node", score: "Score", state: "State", inUse: "In use", standby: "Standby", resting: "Cooling down", observing: "Observing", testing: "Re-testing",
    gainText: "{d} faster than baseline (about {x}×)", gainNone: "About the same as baseline", gainPending: "Estimating baseline speed…", gainUnavailable: "Baseline speed unavailable",
    maintTitle: "Data & restore",
    maintNote: "Every time acceleration stops or this app closes, the hosts file is restored and the boot cleanup task removed; if the app exits unexpectedly, a watchdog restores it. The buttons below do it by hand.",
    restoreHosts: "Restore hosts now", clearData: "Clear node memory & settings",
    restored: "hosts restored", cleared: "Cleared", busyMaint: "Stop acceleration first",
    confirmClear: "This clears node history, the game selection and event logs (the language is kept). Nodes will be probed again next time. Continue?",
    modalTitle: "Please quit the miHoYo launcher first",
    modalBody: "The miHoYo launcher is running. It remembers its old download nodes, so acceleration will not take effect until it is closed.",
    step1: "Pause the download in the launcher",
    step2: "Quit the launcher completely (including its icon in the system tray)",
    step3: "Once acceleration is on, reopen the launcher and resume",
    gotIt: "Got it",
    waitingQuit: "Waiting for the launcher to quit…",
    quitDone: "The launcher has quit",
    selectOne: "Select at least one game",
  },
};

const api = () => window.go.main.App;
const $ = (id) => document.getElementById(id);
const CHECK_SVG = '<svg viewBox="0 0 12 12"><path d="M2.2 6.3 4.8 8.8 9.8 3.4" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>';

const state = {
  lang: "zh",
  games: [],
  selected: new Set(),
  status: null,
  events: [],
  unseen: 0,
  stopping: false,
  modal: { open: false, dismissed: false, closeTimer: 0 },
};
const t = (k) => I18N[state.lang][k];

const fmtRate = (v) => (!v || v < 1 ? "0.0" : (v / 1e6).toFixed(1)) + " MB/s";
const fmtBytes = (v) => (!v ? "0 MB" : v >= 1e9 ? (v / 1e9).toFixed(2) + " GB" : (v / 1e6).toFixed(0) + " MB");
function fmtDuration(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), x = s % 60;
  const p = (n) => String(n).padStart(2, "0");
  return h ? `${h}:${p(m)}:${p(x)}` : `${p(m)}:${p(x)}`;
}
function gameName(host) {
  const g = state.games.find((x) => x.host === host);
  return g ? (state.lang === "en" ? g.nameEn : g.nameZh) : host;
}
const isRunning = () => !!(state.status && state.status.running);

function applyLang() {
  document.documentElement.lang = state.lang === "en" ? "en" : "zh";
  document.querySelectorAll("[data-i18n]").forEach((el) => { el.textContent = t(el.dataset.i18n); });
  document.querySelectorAll(".segmented button").forEach((b) => b.classList.toggle("active", b.dataset.lang === state.lang));
  renderGames();
  renderStatus();
  renderEvents(false);
  renderModal();
}

/* ---------- games ---------- */
function renderGames() {
  const box = $("games");
  box.innerHTML = "";
  const locked = isRunning();
  for (const g of state.games) {
    const on = state.selected.has(g.biz);
    const b = document.createElement("button");
    b.className = "game";
    b.setAttribute("role", "checkbox");
    b.setAttribute("aria-checked", on ? "true" : "false");
    b.disabled = locked;
    b.innerHTML = `<span class="check">${CHECK_SVG}</span><span><div class="game-name"></div><div class="game-host"></div></span>`;
    b.querySelector(".game-name").textContent = state.lang === "en" ? g.nameEn : g.nameZh;
    b.querySelector(".game-host").textContent = state.lang === "en" ? g.nameZh : g.nameEn;
    b.addEventListener("click", () => {
      on ? state.selected.delete(g.biz) : state.selected.add(g.biz);
      $("error").hidden = true;
      renderGames();
    });
    box.appendChild(b);
  }
}

/* ---------- status ---------- */
function renderStatus() {
  const st = state.status || {};
  const running = isRunning();
  const p = st.phase;
  const on = running && (p === "running" || p === "optimizing" || p === "optimized");
  const preparing = running && !on && p !== "stopping";

  $("pill").className = "state " + (on ? "on" : running ? "prep" : "off");
  $("pillText").textContent = on ? t("pillOn") : running ? t("pillPrep") : t("pillOff");

  const downloading = running && (st.launcher === "started" || st.rate > 1e5);
  let phase = running && p ? t("phase")[p] || "" : p === "stopped" ? t("phase").stopped : t("idleHint");
  if (on && downloading) phase = p === "optimizing" ? t("phase").optimizing : "";
  $("phase").textContent = phase;

  const btn = $("toggle");
  btn.disabled = state.stopping || preparing;
  btn.textContent = state.stopping ? t("stopping") : preparing ? t("preparing") : running ? t("stop") : t("start");
  btn.classList.toggle("stop", running && !preparing);

  const err = $("error");
  if (st.error) { err.hidden = false; err.textContent = st.error; }

  $("speedNum").textContent = running ? (st.rate / 1e6 || 0).toFixed(1) : "0.0";

  // Gain over the estimated original speed, for games that are downloading.
  let base = 0, live = 0, measured = false, pending = false;
  for (const h of (running && st.hosts) || []) {
    if (h.rate > 1e6) {
      live += h.rate;
      if (h.baseline > 0) { base += h.baseline; measured = true; }
      else if (h.baseState !== "failed") pending = true;
    }
  }
  const gain = $("gain");
  if (running && measured && live > 0) {
    const d = live - base;
    gain.className = "gain" + (d > 0 ? "" : " dim");
    gain.textContent = d > 0 ? t("gainText").replace("{d}", "+" + fmtRate(d)).replace("{x}", (live / Math.max(base, 1)).toFixed(1)) : t("gainNone");
  } else {
    gain.className = "gain dim";
    gain.textContent = running && live > 0 ? (pending ? t("gainPending") : t("gainUnavailable")) : "";
  }

  $("traffic").textContent = st.bytes ? fmtBytes(st.bytes) : "—";
  $("uptime").textContent = running && st.started && !st.started.startsWith("0001") ? fmtDuration(Date.now() - new Date(st.started).getTime()) : "—";
  const lv = $("launcher");
  lv.textContent = running ? t("launcherState")[st.launcher || ""] : "—";
  lv.className = "strip-value" + (running && st.launcher === "started" ? " ok" : running && st.launcher === "paused" ? " warn" : "");
  const hist = (running && st.history) || [];
  $("peak").textContent = hist.length ? fmtRate(Math.max(...hist)) : "—";

  drawChart(hist);
  renderHosts((running && st.hosts) || []);
  document.querySelectorAll(".maint-actions .btn").forEach((b) => { b.disabled = running || state.stopping; });
}

function renderHosts(hosts) {
  const box = $("hosts");
  box.innerHTML = "";
  for (const h of hosts) {
    const sec = document.createElement("section");
    sec.className = "block";
    const known = (h.nodes || []).length > 0;
    const head = document.createElement("div");
    head.className = "host-head";
    const name = document.createElement("span");
    name.className = "host-name";
    name.textContent = gameName(h.host);
    const tag = document.createElement("span");
    tag.className = "tag";
    tag.textContent = known ? t("proto")[h.mode] || h.mode : t("detecting");
    head.append(name, tag, kv(t("rate"), fmtRate(h.rate)), kv(t("conns"), h.active), kv(t("bytes"), fmtBytes(h.bytes)), kv(t("switches"), h.switches));
    if (h.baseline > 0) head.append(kv(t("baseline"), fmtRate(h.baseline)));
    sec.appendChild(head);

    if (!known) {
      const e = document.createElement("div");
      e.className = "empty";
      e.textContent = t("phase").discovering;
      sec.appendChild(e);
    } else {
      const table = document.createElement("table");
      table.innerHTML = `<thead><tr><th>${t("node")}</th><th>${t("protocol")}</th><th class="r">${t("score")}</th><th class="r">${t("conns")}</th><th>${t("state")}</th></tr></thead>`;
      const tb = document.createElement("tbody");
      for (const n of h.nodes) {
        const cls = n.Testing ? "test" : n.Observing ? "watch" : n.Cooling ? "rest" : n.Active > 0 ? "use" : "";
        const label = n.Testing ? t("testing") : n.Observing ? t("observing") : n.Cooling ? t("resting") : n.Active > 0 ? t("inUse") : t("standby");
        const tr = document.createElement("tr");
        tr.innerHTML = `<td></td><td>${n.H2 ? "HTTP/2" : "HTTP/1.1"}</td><td class="r">${fmtRate(n.Score)}</td><td class="r">${n.Active}</td><td><span class="st ${cls}">${label}</span></td>`;
        tr.firstChild.textContent = n.IP;
        tb.appendChild(tr);
      }
      table.appendChild(tb);
      sec.appendChild(table);
    }
    box.appendChild(sec);
  }
}
function kv(k, v) {
  const s = document.createElement("span");
  s.className = "kv";
  s.append(k);
  const b = document.createElement("b");
  b.textContent = String(v);
  s.appendChild(b);
  return s;
}

function drawChart(history) {
  const canvas = $("chart");
  const dpr = window.devicePixelRatio || 1;
  const w = canvas.clientWidth, h = 132;
  canvas.width = w * dpr;
  canvas.height = h * dpr;
  const ctx = canvas.getContext("2d");
  ctx.scale(dpr, dpr);
  const css = getComputedStyle(document.documentElement);
  const N = 300;
  const data = history.slice(-N);
  const top = Math.max(10e6, Math.ceil(Math.max(0, ...data) / 10e6) * 10e6);
  ctx.strokeStyle = css.getPropertyValue("--stroke").trim();
  ctx.fillStyle = css.getPropertyValue("--text-3").trim();
  ctx.font = "11px 'Segoe UI', sans-serif";
  ctx.lineWidth = 1;
  for (let i = 0; i <= 2; i++) {
    const y = Math.round(h - 1 - (h - 16) * (i / 2)) + 0.5;
    ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(w, y); ctx.stroke();
    if (i) ctx.fillText(((top * i) / 2 / 1e6).toFixed(0) + " MB/s", 2, y - 4);
  }
  if (data.length < 2) return;
  const x = (i) => w - (data.length - 1 - i) * (w / (N - 1));
  const y = (v) => h - 1 - (h - 16) * (v / top);
  ctx.beginPath();
  ctx.moveTo(x(0), h);
  data.forEach((v, i) => ctx.lineTo(x(i), y(v)));
  ctx.lineTo(x(data.length - 1), h);
  ctx.closePath();
  ctx.fillStyle = css.getPropertyValue("--chart-fill").trim();
  ctx.fill();
  ctx.beginPath();
  data.forEach((v, i) => (i ? ctx.lineTo(x(i), y(v)) : ctx.moveTo(x(i), y(v))));
  ctx.strokeStyle = css.getPropertyValue("--chart").trim();
  ctx.lineWidth = 1.5;
  ctx.stroke();
}

/* ---------- events: chat order, follow the bottom unless reading ---------- */
function renderEvents(fromNew) {
  const list = $("events");
  const atBottom = list.scrollHeight - list.scrollTop - list.clientHeight < 24;
  list.innerHTML = "";
  if (!state.events.length) {
    const li = document.createElement("li");
    li.className = "note";
    li.textContent = t("noEvents");
    list.appendChild(li);
    state.unseen = 0;
  } else {
    for (const e of state.events.slice(-300)) {
      const li = document.createElement("li");
      li.className = e.kind;
      const time = document.createElement("time");
      time.textContent = e.time;
      const span = document.createElement("span");
      let text = state.lang === "en" ? e.en : e.zh;
      if (e.host) text = text.split(e.host).join(gameName(e.host));
      span.textContent = text;
      li.append(time, span);
      list.appendChild(li);
    }
    if (!fromNew || atBottom) { list.scrollTop = list.scrollHeight; state.unseen = 0; }
    else state.unseen++;
  }
  const btn = $("newEvents");
  btn.hidden = !state.unseen;
  btn.textContent = state.lang === "en" ? `${state.unseen} new ↓` : `${state.unseen} 条新事件 ↓`;
}

/* ---------- launcher dialog ---------- */
function openModal() {
  if (state.modal.open) return;
  state.modal.open = true;
  clearTimeout(state.modal.closeTimer);
  renderModal();
}
function closeModal() {
  state.modal.open = false;
  state.modal.dismissed = true;
  clearTimeout(state.modal.closeTimer);
  renderModal();
}
function renderModal() {
  $("modal").hidden = !state.modal.open;
  if (!state.modal.open) return;
  const st = state.status || {};
  const box = $("modalState");
  if (st.launcherRunning === false) {
    box.className = "modal-state done";
    box.textContent = "✓ " + t("quitDone");
    clearTimeout(state.modal.closeTimer);
    state.modal.closeTimer = setTimeout(closeModal, 1200);
  } else {
    box.className = "modal-state";
    box.innerHTML = '<span class="spinner"></span><span></span>';
    box.lastChild.textContent = t("waitingQuit");
  }
}

/* ---------- actions ---------- */
async function toggle() {
  if (isRunning()) {
    state.stopping = true;
    renderStatus();
    try { await api().Stop(); } finally { state.stopping = false; await poll(); }
    return;
  }
  if (!state.selected.size) {
    $("error").hidden = false;
    $("error").textContent = t("selectOne");
    return;
  }
  $("error").hidden = true;
  state.events = [];
  renderEvents(false);
  state.modal.dismissed = false;
  try {
    await api().Start([...state.selected]);
  } catch (e) {
    $("error").hidden = false;
    $("error").textContent = String(e);
  }
  if (await api().LauncherRunning()) openModal();
  await poll();
}

async function maintenance(fn, okText) {
  const msg = $("maintMsg");
  if (isRunning()) { msg.textContent = t("busyMaint"); return; }
  try { await fn(); msg.textContent = okText; } catch (e) { msg.textContent = String(e); }
  setTimeout(() => { msg.textContent = ""; }, 4000);
}

function onStatus(st) {
  const wasRunning = isRunning();
  state.status = st;
  // Re-open the dialog if a run is waiting on the launcher and the user has not dismissed it.
  if (st.running && st.phase === "wait_launcher" && !state.modal.dismissed) openModal();
  renderStatus();
  renderModal();
  if (wasRunning !== !!st.running) renderGames();
}

async function poll() {
  try { onStatus(await api().Status()); } catch (e) { /* the back end may be busy */ }
}

async function init() {
  const s = await api().GetSettings();
  state.lang = s.lang || "zh";
  state.games = await api().Games();
  const saved = s.games || [];
  for (const g of state.games) {
    if (saved.length ? saved.includes(g.biz) : g.installed || g.pending) state.selected.add(g.biz);
  }
  state.events = await api().Events();

  document.querySelectorAll(".segmented button").forEach((b) =>
    b.addEventListener("click", () => { state.lang = b.dataset.lang; api().SetLang(state.lang); applyLang(); }));
  $("toggle").addEventListener("click", toggle);
  $("modalOk").addEventListener("click", closeModal);
  $("restoreHosts").addEventListener("click", () => maintenance(() => api().RestoreHosts(), t("restored")));
  $("clearData").addEventListener("click", async () => {
    if (isRunning()) { $("maintMsg").textContent = t("busyMaint"); return; }
    if (await api().Confirm(t("clearData"), t("confirmClear"))) {
      maintenance(async () => { await api().ClearData(); state.events = []; renderEvents(false); }, t("cleared"));
    }
  });
  $("newEvents").addEventListener("click", () => {
    const l = $("events"); l.scrollTop = l.scrollHeight; state.unseen = 0; $("newEvents").hidden = true;
  });
  $("events").addEventListener("scroll", () => {
    const l = $("events");
    if (state.unseen && l.scrollHeight - l.scrollTop - l.clientHeight < 24) { state.unseen = 0; $("newEvents").hidden = true; }
  });
  window.runtime.EventsOn("event", (e) => {
    state.events.push(e);
    if (state.events.length > 500) state.events.shift();
    renderEvents(true);
  });
  window.runtime.EventsOn("status", onStatus);
  window.runtime.EventsOn("stopped", poll);
  window.addEventListener("resize", renderStatus);

  applyLang();
  await poll();
  if (await api().LauncherRunning()) openModal(); // warn right away on start-up
  setInterval(poll, 5000);
}

(function waitForRuntime() {
  if (window.go && window.go.main && window.runtime) init();
  else setTimeout(waitForRuntime, 30);
})();
