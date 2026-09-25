// ProSAFE Plus desktop UI. Talks to the Go sidecar's JSON API. Falls back to
// a same-origin base when run in a plain browser (vite dev) with the Go server
// started manually via `prosafe serve --listen 127.0.0.1:8848`.
let API = null;
let current = null;          // selected device (mac)
let devices = [];
let page = "info";
let loggedIn = new Set();

const PAGES = [
  ["info", "Info"], ["status", "Status"], ["stats", "Statistics"],
  ["vlan", "VLAN"], ["qos", "QoS"], ["lag", "LAG"], ["multicast", "Multicast"],
  ["management", "Management"], ["mirror", "Mirror"], ["cable", "Cable Test"],
  ["maint", "Maintenance"],
];

const $ = (s) => document.querySelector(s);
const el = (t, c, h) => { const e = document.createElement(t); if (c) e.className = c; if (h != null) e.innerHTML = h; return e; };

async function resolveApi() {
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    for (let i = 0; i < 40; i++) {
      const base = await invoke("api_base");
      if (base) return base.replace(/\/$/, "");
      await new Promise((r) => setTimeout(r, 250));
    }
  } catch (_) { /* not in Tauri */ }
  return "http://127.0.0.1:8848"; // browser dev fallback
}

async function api(path, opts = {}) {
  const r = await fetch(API + path, {
    headers: { "Content-Type": "application/json" }, ...opts,
  });
  const text = await r.text();
  let data; try { data = text ? JSON.parse(text) : {}; } catch { data = { raw: text }; }
  if (!r.ok) throw new Error(data.error || r.statusText);
  return data;
}
const post = (p, body) => api(p, { method: "POST", body: JSON.stringify(body || {}) });

function toast(msg, kind = "ok") {
  const t = $("#toast"); t.textContent = msg; t.className = "toast show " + kind;
  setTimeout(() => (t.className = "toast"), 3200);
}
function setConn(state, text) {
  $("#conn-dot").className = "dot " + state; $("#conn-text").textContent = text;
}

// ---- devices ---------------------------------------------------------------

function macOf(d) { return (d.device || d).mac; }

async function rescan() {
  setConn("", "scanning…");
  try {
    const list = await post("/api/discover");
    devices = list || [];
    setConn("ok", `${devices.length} switch${devices.length === 1 ? "" : "es"}`);
  } catch (e) { setConn("err", "scan failed"); toast(e.message, "err"); }
  renderDevices();
}

function renderDevices() {
  const box = $("#devices"); box.innerHTML = "";
  if (!devices.length) { box.appendChild(el("div", "muted", "No switches found.")); return; }
  for (const row of devices) {
    const d = row.device || row;
    const li = el("div", "device" + (macOf(row) === current ? " active" : ""));
    const on = loggedIn.has(d.mac) ? "badge on" : "badge";
    li.innerHTML = `<span class="${on}">${loggedIn.has(d.mac) ? "online" : d.password_mode || row.password_mode || ""}</span>
      <div class="model">${d.model || "switch"}</div>
      <div class="meta">${d.name || "unnamed"} · ${d.ip}</div>
      <div class="meta">${d.mac}</div>`;
    li.onclick = () => selectDevice(d);
    box.appendChild(li);
  }
}

async function selectDevice(d) {
  current = d.mac;
  renderDevices();
  if (!loggedIn.has(d.mac)) return login(d);
  openSwitch(d);
}

function login(d) {
  showModal("Log in to " + (d.model || "switch"), `
    <div class="field"><label>${d.ip} · ${d.mac}</label>
    <input type="password" id="pw" placeholder="password" value="password" autofocus></div>
    <div class="muted">Default password is “password”.</div>`, async () => {
    const pw = $("#pw").value;
    await post(`/api/switch/${d.mac}/login`, { password: pw });
    loggedIn.add(d.mac); renderDevices(); openSwitch(d);
    toast("Logged in to " + d.ip);
  });
}

function openSwitch(d) {
  $("#switch-title").innerHTML = `${d.model || "Switch"} <small>${d.ip} · ${d.mac}</small>`;
  renderTabs(); loadPage();
}

function renderTabs() {
  const nav = $("#tabs"); nav.innerHTML = "";
  for (const [id, label] of PAGES) {
    const t = el("div", "tab" + (id === page ? " active" : ""), label);
    t.onclick = () => { page = id; renderTabs(); loadPage(); };
    nav.appendChild(t);
  }
}

// ---- page rendering --------------------------------------------------------

function card(title, inner) { return `<div class="card"><h2>${title}</h2>${inner}</div>`; }
function kv(pairs) { return `<div class="kv">${pairs.map(([k, v]) => `<div class="k">${k}</div><div class="v">${v}</div>`).join("")}</div>`; }
function esc(s) { return String(s ?? "").replace(/[&<>]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" }[c])); }

async function loadPage() {
  const c = $("#content");
  if (!current) { c.innerHTML = `<div class="empty">Select a switch, then log in.</div>`; return; }
  c.innerHTML = `<div class="empty">Loading…</div>`;
  try {
    const mac = current;
    const r = renderers[page];
    c.innerHTML = await r(mac);
    if (afters[page]) afters[page](mac);
  } catch (e) { c.innerHTML = card("Error", `<div class="muted">${esc(e.message)}</div>`); }
}

const renderers = {
  async info(mac) {
    const i = await api(`/api/switch/${mac}/info`);
    return card("Switch Information", kv([
      ["Product", esc(i.model)], ["Name", esc(i.name) || "<span class=muted>(unset)</span>"],
      ["MAC address", esc(i.mac)], ["Serial", esc(i.serial)],
      ["Firmware", `${esc(i.firmware)} (image ${i.active_image})`],
      ["DHCP", i.dhcp ? "On" : "Off"], ["IP address", esc(i.ip)],
      ["Subnet mask", esc(i.netmask)], ["Gateway", esc(i.gateway)],
      ["Ports", i.ports], ["Auth mode", esc(i.password_mode)],
    ])) +
    card("Actions", `<div class="row">
      <button class="btn" onclick="ui.editName('${mac}','${esc(i.name)}')">Rename switch</button>
      <button class="btn" onclick="ui.editIP('${mac}',${JSON.stringify({dhcp:i.dhcp,ip:i.ip,netmask:i.netmask,gateway:i.gateway}).replace(/"/g,'&quot;')})">IP settings…</button>
    </div>`);
  },
  async status(mac) {
    const d = await api(`/api/switch/${mac}/ports`);
    const rows = d.ports.map((p) => `<tr><td>${p.port}</td>
      <td><span class="pill ${p.up ? "up" : "down"}">${p.up ? "Up" : "Down"}</span></td>
      <td>${esc(p.speed_name)}</td>
      <td><label class="toggle"><input type="checkbox" ${p.flow_control ? "checked" : ""}
        onchange="ui.flow('${mac}',${p.port},this.checked)"><span class="track"></span></label></td></tr>`).join("");
    return card("Port Status", `<table><thead><tr><th>Port</th><th>Link</th><th>Speed</th><th>Flow control</th></tr></thead><tbody>${rows}</tbody></table>`);
  },
  async stats(mac) {
    const s = await api(`/api/switch/${mac}/stats`);
    const rows = s.map((p) => `<tr><td>${p.port}</td><td>${p.rx_bytes.toLocaleString()}</td>
      <td>${p.tx_bytes.toLocaleString()}</td><td>${p.crc_errors.toLocaleString()}</td></tr>`).join("");
    return card("Port Statistics", `<table><thead><tr><th>Port</th><th>RX bytes</th><th>TX bytes</th><th>CRC errors</th></tr></thead><tbody>${rows}</tbody></table>
      <div class="row"><button class="btn danger" onclick="ui.clearStats('${mac}')">Clear counters</button>
      <button class="btn ghost" onclick="ui.reload()">Refresh</button></div>`);
  },
  async vlan(mac) {
    const d = await api(`/api/switch/${mac}/vlan`);
    const cfg = d.config;
    const opts = d.modes.map((n, i) => `<option value="${i}" ${i === cfg.mode ? "selected" : ""}>${esc(n)}</option>`).join("");
    let body = `<div class="row"><div class="field"><label>Engine mode</label>
      <select onchange="ui.vlanMode('${mac}',this.value)">${opts}</select></div></div>`;
    if (cfg.port_vlans && cfg.port_vlans.length) {
      body += `<h2 style="margin-top:18px">Port-based VLANs</h2><table><thead><tr><th>VLAN</th><th>Ports</th></tr></thead><tbody>${
        cfg.port_vlans.map((v) => `<tr><td>${v.vid}</td><td>${(v.ports||[]).join(", ")||"-"}</td></tr>`).join("")}</tbody></table>`;
    }
    if (cfg.vlans_8021q && cfg.vlans_8021q.length) {
      body += `<h2 style="margin-top:18px">802.1Q VLANs</h2><table><thead><tr><th>VID</th><th>Untagged</th><th>Tagged</th><th></th></tr></thead><tbody>${
        cfg.vlans_8021q.map((v) => { const t = new Set(v.tagged||[]); const un=(v.members||[]).filter(p=>!t.has(p));
          return `<tr><td>${v.vid}</td><td>${un.join(", ")||"-"}</td><td>${(v.tagged||[]).join(", ")||"-"}</td>
          <td><button class="btn ghost" onclick="ui.delVlan('${mac}',${v.vid})">✕</button></td></tr>`; }).join("")}</tbody></table>
        <div class="row"><button class="btn" onclick="ui.addVlan('${mac}')">Add / edit 802.1Q VLAN</button></div>`;
    }
    return card("VLAN Configuration", body);
  },
  async qos(mac) {
    const d = await api(`/api/switch/${mac}/qos`);
    const cfg = d.config, rates = d.rates, prios = d.priorities;
    const rate = (arr, port) => { const r = (arr||[]).find((x) => x.port === port); return r ? rates[r.rate] : "-"; };
    const prioName = (port) => { const p = (cfg.priorities||[]).find((x) => x.port === port); return p ? prios[p.priority-1] : "-"; };
    const ports = []; for (let i = 1; i <= 32; i++) if ((cfg.ingress||[]).some(x=>x.port===i)||(cfg.priorities||[]).some(x=>x.port===i)) ports.push(i);
    const rows = ports.map((i) => `<tr><td>${i}</td><td>${prioName(i)}</td><td>${rate(cfg.ingress,i)}</td><td>${rate(cfg.egress,i)}</td><td>${rate(cfg.storm_rates,i)}</td></tr>`).join("");
    return card("Quality of Service", kv([["Mode", esc(cfg.mode_name)], ["Broadcast filtering", cfg.broadcast_filter ? "On" : "Off"]]) +
      `<table style="margin-top:14px"><thead><tr><th>Port</th><th>Priority</th><th>Ingress</th><th>Egress</th><th>Storm</th></tr></thead><tbody>${rows}</tbody></table>
      <div class="row">
        <button class="btn" onclick="ui.qosMode('${mac}',${cfg.mode})">Toggle mode</button>
        <button class="btn" onclick="ui.qosPriority('${mac}')">Set priority…</button>
        <button class="btn" onclick="ui.qosRate('${mac}',${JSON.stringify(rates).replace(/"/g,'&quot;')})">Rate limit…</button>
        <label class="toggle"><input type="checkbox" ${cfg.broadcast_filter?"checked":""} onchange="ui.broadcast('${mac}',this.checked)"><span class="track"></span></label>
        <span class="muted">Broadcast storm filtering</span>
      </div>`);
  },
  async lag(mac) {
    const d = await api(`/api/switch/${mac}/lags`);
    if (!d.supported) return card("Link Aggregation", `<div class="muted">This switch model does not support LAG.</div>`);
    const rows = d.lags.map((l) => `<tr><td>${l.id}</td>
      <td><label class="toggle"><input type="checkbox" ${l.enabled?"checked":""} onchange="ui.lagToggle('${mac}',${l.id},this.checked,'${(l.ports||[]).join(",")}')"><span class="track"></span></label></td>
      <td>${(l.ports||[]).join(", ")||"-"}</td>
      <td><button class="btn ghost" onclick="ui.lagEdit('${mac}',${l.id},'${(l.ports||[]).join(",")}',${l.enabled})">Edit</button></td></tr>`).join("");
    return card("Static Link Aggregation", `<table><thead><tr><th>LAG</th><th>Admin</th><th>Ports</th><th></th></tr></thead><tbody>${rows}</tbody></table>
      <div class="muted" style="margin-top:10px">Static aggregation only — this firmware has no LACP. Configure the peer as a static trunk too.</div>`);
  },
  async multicast(mac) {
    const m = await api(`/api/switch/${mac}/multicast`);
    return card("IGMP Snooping", `
      ${toggleRow("Snooping", m.snooping, `ui.mcast('${mac}','snooping',this.checked)`)}
      <div class="row"><div class="field"><label>Snooping VLAN (1–4094)</label>
        <input type="number" id="mvid" value="${m.vid}" min="1" max="4094" style="width:120px"
        onchange="ui.mcastVid('${mac}',this.value)"></div></div>
      ${toggleRow("Validate IGMPv3 header", m.validate_igmpv3, `ui.mcast('${mac}','validate_igmpv3',this.checked)`)}
      ${toggleRow("Block unknown multicast", m.block_unknown, `ui.mcast('${mac}','block_unknown',this.checked)`)}
      ${m.router_ports_supported ? kv([["Static router ports", (m.router_ports||[]).join(", ")||"-"]]) : ""}`);
  },
  async management(mac) {
    const m = await api(`/api/switch/${mac}/management`);
    let body = toggleRow("Loop detection", m.loop_detection, `ui.mgmt('${mac}','LoopDetection',this.checked,${JSON.stringify(m).replace(/"/g,"&quot;")})`);
    if (m.power_saving != null) body += toggleRow("Power saving", m.power_saving, `ui.mgmtPower('${mac}',this.checked)`);
    if (m.port_led != null) body += toggleRow("Port LEDs", m.port_led, `ui.mgmtLed('${mac}',this.checked)`);
    return card("Management", body);
  },
  async mirror(mac) {
    const m = await api(`/api/switch/${mac}/mirror`);
    return card("Port Mirroring", kv([
      ["Status", m.dest ? "Enabled" : "Disabled"],
      ["Destination", m.dest || "-"], ["Sources", (m.sources||[]).join(", ") || "-"],
    ]) + `<div class="row"><button class="btn" onclick="ui.mirror('${mac}')">Configure…</button>
      ${m.dest ? `<button class="btn danger" onclick="ui.mirrorOff('${mac}')">Disable</button>` : ""}</div>`);
  },
  async cable(mac) {
    return card("Cable Tester", `<div class="muted">Select ports and run the tester. A live cable reads a distance; a break reads the fault distance.</div>
      <div class="row"><input type="text" id="cports" placeholder="1,2,5-8" style="width:160px">
      <button class="btn primary" onclick="ui.cable('${mac}')">Test</button></div>
      <div id="cable-results"></div>`);
  },
  async maint(mac) {
    const i = await api(`/api/switch/${mac}/info`);
    return card("Maintenance", kv([["Firmware", esc(i.firmware)], ["Serial", esc(i.serial)]]) + `
      <div class="row">
        <button class="btn" onclick="ui.passwd('${mac}')">Change password</button>
        <button class="btn" onclick="ui.saveCfg('${mac}')">Save config…</button>
        <button class="btn" onclick="ui.restoreCfg('${mac}')">Restore config…</button>
        <button class="btn" onclick="ui.firmware('${mac}')">Upgrade firmware…</button>
      </div>
      <div class="row">
        <button class="btn danger" onclick="ui.reboot('${mac}')">Reboot</button>
        <button class="btn danger" onclick="ui.factory('${mac}')">Factory reset</button>
      </div>`);
  },
};

const afters = {};

function toggleRow(label, on, handler) {
  return `<div class="row"><label class="toggle"><input type="checkbox" ${on?"checked":""} onchange="${handler}"><span class="track"></span></label><span>${label}</span></div>`;
}

// ---- modal -----------------------------------------------------------------

let modalOK = null;
function showModal(title, bodyHtml, onOk) {
  $("#modal-title").textContent = title; $("#modal-body").innerHTML = bodyHtml;
  modalOK = onOk; $("#modal").classList.remove("hidden");
  const f = $("#modal-body").querySelector("input,select"); if (f) f.focus();
}
function hideModal() { $("#modal").classList.add("hidden"); modalOK = null; }

// ---- ui actions (called from inline handlers) ------------------------------

async function guard(fn) { try { await fn(); } catch (e) { toast(e.message, "err"); } }

const ui = {
  reload: () => loadPage(),
  editName: (mac, name) => showModal("Rename switch",
    `<div class="field"><label>Name</label><input id="v" type="text" value="${esc(name)}"></div>`,
    async () => { await post(`/api/switch/${mac}/name`, { name: $("#v").value }); toast("Name updated"); loadPage(); }),
  editIP: (mac, cur) => showModal("IP settings", `
    ${toggleRow("Use DHCP", cur.dhcp, "")}
    <div class="field"><label>IP address</label><input id="ip" type="text" value="${esc(cur.ip)}"></div>
    <div class="field"><label>Subnet mask</label><input id="nm" type="text" value="${esc(cur.netmask)}"></div>
    <div class="field"><label>Gateway</label><input id="gw" type="text" value="${esc(cur.gateway)}"></div>`,
    async () => {
      const dhcp = $("#modal-body input[type=checkbox]").checked;
      await post(`/api/switch/${mac}/ip`, { dhcp, ip: $("#ip").value, netmask: $("#nm").value, gateway: $("#gw").value });
      toast("IP settings applied — switch may move to a new address"); rescan();
    }),
  flow: (mac, port, on) => guard(async () => { await post(`/api/switch/${mac}/ports/admin`, { port, speed: 0, flow_control: on }); toast(`Port ${port} flow control ${on?"on":"off"}`); }),
  clearStats: (mac) => guard(async () => { await post(`/api/switch/${mac}/stats/clear`); toast("Counters cleared"); loadPage(); }),
  vlanMode: (mac, mode) => guard(async () => { await post(`/api/switch/${mac}/vlan/mode`, { mode: +mode }); toast("VLAN mode changed (membership reset)"); loadPage(); }),
  addVlan: (mac) => showModal("Add / edit 802.1Q VLAN", `
    <div class="field"><label>VLAN ID</label><input id="vid" type="number" min="1" max="4094"></div>
    <div class="field"><label>Member ports</label><input id="mem" type="text" placeholder="1,2,5-8"></div>
    <div class="field"><label>Tagged ports</label><input id="tag" type="text" placeholder="5-8"></div>`,
    async () => {
      const members = parsePorts($("#mem").value), tagged = parsePorts($("#tag").value);
      await post(`/api/switch/${mac}/vlan/8021q`, { vid: +$("#vid").value, members, tagged });
      toast("VLAN saved"); loadPage();
    }),
  delVlan: (mac, vid) => guard(async () => { await post(`/api/switch/${mac}/vlan/8021q/delete`, { vid }); toast(`VLAN ${vid} deleted`); loadPage(); }),
  qosMode: (mac, cur) => guard(async () => { await post(`/api/switch/${mac}/qos/mode`, { mode: cur === 2 ? 1 : 2 }); toast("QoS mode changed"); loadPage(); }),
  qosPriority: (mac) => showModal("Set port priority", `
    <div class="field"><label>Ports</label><input id="p" type="text" placeholder="1,2"></div>
    <div class="field"><label>Priority</label><select id="pr"><option value="1">High</option><option value="2">Medium</option><option value="3" selected>Normal</option><option value="4">Low</option></select></div>`,
    async () => { await post(`/api/switch/${mac}/qos/priority`, { ports: parsePorts($("#p").value), priority: +$("#pr").value }); toast("Priority set"); loadPage(); }),
  qosRate: (mac, rates) => { const opts = rates.map((n, i) => `<option value="${i}">${n}</option>`).join("");
    showModal("Rate limit", `<div class="field"><label>Ports</label><input id="p" type="text" placeholder="1,2"></div>
      <div class="grid2"><div class="field"><label>Ingress</label><select id="in">${opts}</select></div>
      <div class="field"><label>Egress</label><select id="eg">${opts}</select></div></div>`,
      async () => { await post(`/api/switch/${mac}/qos/rate`, { ports: parsePorts($("#p").value), ingress: +$("#in").value, egress: +$("#eg").value }); toast("Rate limit set"); loadPage(); }); },
  broadcast: (mac, on) => guard(async () => { await post(`/api/switch/${mac}/qos/broadcast`, { enabled: on }); toast("Broadcast filter " + (on?"on":"off")); }),
  lagToggle: (mac, id, on, ports) => guard(async () => { await post(`/api/switch/${mac}/lags`, { id, enabled: on, ports: parsePorts(ports) }); toast(`LAG ${id} ${on?"enabled":"disabled"}`); loadPage(); }),
  lagEdit: (mac, id, ports, enabled) => showModal(`Edit LAG ${id}`, `
    <div class="field"><label>Member ports</label><input id="p" type="text" value="${ports}"></div>
    ${toggleRow("Enabled", enabled, "")}`,
    async () => { const on = $("#modal-body input[type=checkbox]").checked;
      await post(`/api/switch/${mac}/lags`, { id, enabled: on, ports: parsePorts($("#p").value) }); toast("LAG saved"); loadPage(); }),
  mcast: (mac, key, val) => guard(async () => { const m = await api(`/api/switch/${mac}/multicast`); m[key] = val; await post(`/api/switch/${mac}/multicast`, m); toast("Multicast updated"); }),
  mcastVid: (mac, v) => guard(async () => { const m = await api(`/api/switch/${mac}/multicast`); m.vid = +v; await post(`/api/switch/${mac}/multicast`, m); toast("Snooping VLAN set"); }),
  mgmt: (mac, key, val) => guard(async () => { await post(`/api/switch/${mac}/management`, { loop_detection: val }); toast("Management updated"); }),
  mgmtPower: (mac, on) => guard(async () => { const m = await api(`/api/switch/${mac}/management`); m.power_saving = on; await post(`/api/switch/${mac}/management`, m); toast("Power saving " + (on?"on":"off")); }),
  mgmtLed: (mac, on) => guard(async () => { const m = await api(`/api/switch/${mac}/management`); m.port_led = on; await post(`/api/switch/${mac}/management`, m); toast("Port LEDs " + (on?"on":"off")); }),
  mirror: (mac) => showModal("Port mirroring", `
    <div class="field"><label>Destination port (0 disables)</label><input id="d" type="number" value="0"></div>
    <div class="field"><label>Source ports</label><input id="s" type="text" placeholder="1,2,3"></div>`,
    async () => { await post(`/api/switch/${mac}/mirror`, { dest: +$("#d").value, sources: parsePorts($("#s").value) }); toast("Mirror set"); loadPage(); }),
  mirrorOff: (mac) => guard(async () => { await post(`/api/switch/${mac}/mirror`, { dest: 0, sources: [] }); toast("Mirroring disabled"); loadPage(); }),
  cable: (mac) => guard(async () => {
    $("#cable-results").innerHTML = `<div class="muted" style="margin-top:12px">Testing… this takes a few seconds per port.</div>`;
    const r = await post(`/api/switch/${mac}/cabletest`, { ports: parsePorts($("#cports").value) });
    const rows = (r.results||[]).map((c) => `<tr><td>${c.port}</td><td>${esc(c.status_name)}</td><td>${c.distance_m}</td></tr>`).join("");
    $("#cable-results").innerHTML = `<table style="margin-top:14px"><thead><tr><th>Port</th><th>Result</th><th>Fault distance (m)</th></tr></thead><tbody>${rows}</tbody></table>` + (r.error ? `<div class="muted">${esc(r.error)}</div>` : "");
  }),
  passwd: (mac) => showModal("Change password", `
    <div class="field"><label>New password</label><input id="np" type="password"></div>
    <div class="field"><label>Confirm</label><input id="cp" type="password"></div>`,
    async () => { if ($("#np").value !== $("#cp").value) throw new Error("passwords do not match");
      await post(`/api/switch/${mac}/password`, { old: "", new: $("#np").value }); toast("Password changed"); }),
  saveCfg: (mac) => guard(async () => {
    const path = await savePath("switch-config.json"); if (!path) return;
    await post(`/api/switch/${mac}/config/save`, { path }); toast("Configuration saved");
  }),
  restoreCfg: (mac) => guard(async () => {
    const path = await openPath(); if (!path) return;
    if (!confirm("Apply configuration from this file?")) return;
    const r = await post(`/api/switch/${mac}/config/restore`, { path });
    toast(r.ok ? "Configuration restored" : ("Restore: " + (r.error||"error")), r.ok ? "ok" : "err");
  }),
  firmware: (mac) => guard(async () => {
    const path = await openPath(); if (!path) return;
    if (!confirm("Flash this firmware image? Do not power off during the upgrade.")) return;
    toast("Uploading firmware…");
    const r = await post(`/api/switch/${mac}/firmware`, { path });
    toast(r.ok ? "Firmware uploaded — switch will reboot" : ("Firmware: " + (r.error||"error")), r.ok ? "ok" : "err");
  }),
  reboot: (mac) => guard(async () => { if (!confirm("Reboot the switch?")) return; await post(`/api/switch/${mac}/reboot`); toast("Reboot requested"); }),
  factory: (mac) => guard(async () => { if (!confirm("FACTORY RESET erases all configuration. Continue?")) return; await post(`/api/switch/${mac}/factory-reset`); toast("Factory reset requested"); }),
};
window.ui = ui;

function parsePorts(s) {
  const out = []; if (!s) return out;
  for (const part of s.split(",")) {
    const m = part.trim().match(/^(\d+)-(\d+)$/);
    if (m) { for (let i = +m[1]; i <= +m[2]; i++) out.push(i); }
    else if (part.trim()) out.push(+part.trim());
  }
  return out;
}

async function savePath(name) {
  try { const { save } = await import("@tauri-apps/plugin-dialog"); return await save({ defaultPath: name }); }
  catch { return prompt("Save to path:", name); }
}
async function openPath() {
  try { const { open } = await import("@tauri-apps/plugin-dialog"); return await open({ multiple: false }); }
  catch { return prompt("File path:"); }
}

// ---- boot ------------------------------------------------------------------

async function boot() {
  API = await resolveApi();
  setConn("ok", "ready");
  $("#rescan").onclick = rescan;
  $("#addip").onclick = () => showModal("Add switch by IP",
    `<div class="field"><label>IP address</label><input id="v" type="text" placeholder="192.168.0.239"></div>`,
    async () => { await post("/api/devices/add", { ip: $("#v").value }); toast("Added"); const d = await api("/api/devices"); devices = d; renderDevices(); });
  $("#modal-cancel").onclick = hideModal;
  $("#modal-ok").onclick = async () => { const fn = modalOK; if (!fn) return hideModal(); try { await fn(); hideModal(); } catch (e) { toast(e.message, "err"); } };
  $("#modal").addEventListener("keydown", (e) => { if (e.key === "Enter") $("#modal-ok").click(); if (e.key === "Escape") hideModal(); });
  renderTabs();
  rescan();
}
boot();
