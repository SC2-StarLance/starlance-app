const svc = window.go && window.go.main && window.go.main.WindowService;

// Update phases come from Go (update.go): the small window opens for
// "downloading" and says "Restarting" while the process hands over.
function onUpdatePhase(p) {
  const box = document.getElementById("updating");
  if (!box || !p || !p.phase) return;
  if (p.phase === "downloading" || p.phase === "installed" || p.phase === "restarting") {
    box.classList.remove("hidden");
    document.getElementById("updatingVersion").textContent = p.version ? "version " + p.version : "";
    document.getElementById("updatingPhase").textContent =
      p.phase === "downloading" ? "Downloading the new version" : "Restarting the StarLance App";
    return;
  }
  if (p.phase === "failed" || p.phase === "idle") box.classList.add("hidden");
}
if (window.runtime && window.runtime.EventsOn) {
  window.runtime.EventsOn("update-phase", onUpdatePhase);
}
const $ = (id) => document.getElementById(id);

let boot = { loggedIn: false, setupDone: false, name: "", regions: [], folders: [], startWithSystem: false, replays_public: true, version: "", ranks: {}, stats: {} };
let state = { kind: "idle", elapsed: 0, at: 0, counters: {}, pending: null, queuePreference: "", voidSeenAt: null, lockUntil: 0, lockLabel: "Leave penalty" };
let race = localStorage.getItem("sl.race") || "zerg";
let wanted = JSON.parse(localStorage.getItem("sl.regions") || "null");
// One preferred region, not a set: a star is a radio choice (spec 2.31.0), used
// when both players share more than one region.
// Older builds stored an array here, so take its first entry and move on.
let preferred = (() => {
  const raw = localStorage.getItem("sl.preferred");
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw);
    if (typeof parsed === "string") return parsed;
    if (Array.isArray(parsed) && typeof parsed[0] === "string") return parsed[0];
  } catch {
    return raw;
  }
  return null;
})();
let poll = null;
let bootRetry = null;

const QUEUE_MODES = ["any", "ranked_only", "unranked_only", "ranked_preferred", "unranked_preferred"];
let queueMode = (() => {
  const raw = localStorage.getItem("sl.queuePreference");
  return QUEUE_MODES.includes(raw) ? raw : "any";
})();

const REGION_LABEL = { us: "NA", eu: "EU", kr: "KR" };
const REGIONS_ALL = ["us", "eu", "kr"];
const LEAGUE_HEX = {
  bronze: "#c98a4b",
  silver: "#b9c6d8",
  gold: "#e6b845",
  platinum: "#d4dce8",
  diamond: "#3fb6f6",
  master: "#6e8bff",
  grandmaster: "#ff8a3d",
};
const LEAGUE_ORDER = ["bronze", "silver", "gold", "platinum", "diamond", "master", "grandmaster"];
let SITE = "https://starlance.club";
let lastKind = "";

function openURL(url) {
  // The Go binding is the path that reliably reaches the real browser; the
  // in-page runtime call is the fallback for older shells.
  if (window.go && window.go.main && window.go.main.WindowService) {
    window.go.main.WindowService.OpenURL(url).catch(() => {});
    return;
  }
  window.open(url, "_blank");
}

function showError(msg) {
  const e = $("error");
  e.textContent = msg || "";
  e.classList.toggle("hidden", !msg);
}

function showSet(msg, isError) {
  $("setNote").textContent = isError ? "" : msg || "";
  $("setNote").classList.toggle("hidden", isError || !msg);
  $("setError").textContent = isError ? msg || "" : "";
  $("setError").classList.toggle("hidden", !isError || !msg);
}

function selectedRegions() {
  const all = boot.regions || [];
  if (!wanted) return all;
  const keep = all.filter((r) => wanted.includes(r));
  return keep.length ? keep : all;
}

function saveRegions(next) {
  wanted = next;
  localStorage.setItem("sl.regions", JSON.stringify(wanted));
}

function savePreferred(next) {
  preferred = next;
  if (next) localStorage.setItem("sl.preferred", JSON.stringify(next));
  else localStorage.removeItem("sl.preferred");
}

function selectedPreference() {
  return selectedRegions().includes(preferred) ? preferred : "";
}

function saveQueueMode(next) {
  queueMode = QUEUE_MODES.includes(next) ? next : "any";
  localStorage.setItem("sl.queuePreference", queueMode);
}

function selectedQueuePreference() {
  return queueMode;
}

function regionHint(text) {
  $("regionHintText").textContent = text;
  $("regionHint").classList.remove("hidden");
}

function hideRegionHint() {
  $("regionHint").classList.add("hidden");
}

$("regionHintOpen").onclick = () => openURL(SITE + "/settings");
$("regionHintCheck").onclick = async () => {
  const b = $("regionHintCheck");
  b.disabled = true;
  const was = b.textContent;
  b.textContent = "Checking";
  try {
    const regions = await svc.VerifyRegions();
    boot.regions = regions || [];
    hideRegionHint();
    renderRegions();
  } catch (_) {
    b.textContent = "Could not check";
    setTimeout(() => (b.textContent = was), 1600);
  } finally {
    b.disabled = false;
    if (b.textContent === "Checking") b.textContent = was;
  }
};

function renderRegions() {
  const wrap = $("regionsWrap");
  const box = $("regions");
  const verified = boot.regions || [];
  wrap.classList.remove("hidden");
  box.innerHTML = "";
  const chosen = selectedRegions();
  const starred = selectedPreference();
  for (const r of REGIONS_ALL) {
    const ok = verified.includes(r);
    const label = REGION_LABEL[r] || r.toUpperCase();
    const b = document.createElement("button");
    b.className = "region" + (ok ? (chosen.includes(r) ? " on" : "") : " unlinked");
    b.dataset.region = r;
    b.textContent = ok ? label : label + " +";
    b.type = "button";
    b.onclick = () => {
      if (!ok) {
        regionHint(
          "Log in to StarCraft II on the " +
            label +
            " server once with this account to create a profile, then check again."
        );
        return;
      }
      hideRegionHint();
      if (state.kind !== "idle") return;
      const cur = selectedRegions();
      let next = cur.includes(r) ? cur.filter((x) => x !== r) : [...cur, r];
      if (next.length === 0) next = [r];
      saveRegions(next);
      // Growing from one region to several starts with no preferred star.
      if (!cur.includes(r) && cur.length === 1) savePreferred(null);
      // A preference can only live on a selected region.
      if (preferred && !next.includes(preferred)) savePreferred(null);
      renderRegions();
    };
    // A star only means something when there is a second region to prefer
    // over; with one selected region the intersection is already fixed.
    if (ok && chosen.includes(r) && chosen.length > 1) {
      const on = starred === r;
      const star = document.createElement("button");
      star.className = "region-star" + (on ? " on" : "");
      star.type = "button";
      star.textContent = on ? "★" : "☆";
      star.setAttribute("aria-pressed", on ? "true" : "false");
      star.setAttribute("aria-label", on
        ? "Stop preferring " + label
        : "Prefer " + label + " when both players share it");
      star.disabled = state.kind !== "idle";
      star.onclick = () => {
        savePreferred(on ? null : r);
        renderRegions();
      };
      const slot = document.createElement("span");
      slot.className = "region-slot";
      slot.appendChild(b);
      slot.appendChild(star);
      box.appendChild(slot);
      continue;
    }
    box.appendChild(b);
  }
  renderStatus();
}

const MODE_LABEL = { ranked: "Ranked", unranked: "Unranked" };

function renderQueueModes() {
  const box = $("queueModes");
  if (!box) return;
  box.innerHTML = "";
  const both = queueMode === "any" || queueMode === "ranked_preferred" || queueMode === "unranked_preferred";
  const rankedOn = queueMode !== "unranked_only";
  const unrankedOn = queueMode !== "ranked_only";
  const locked = state.kind !== "idle";
  for (const mode of ["ranked", "unranked"]) {
    const on = mode === "ranked" ? rankedOn : unrankedOn;
    const b = document.createElement("button");
    b.type = "button";
    b.className = "region" + (on ? " on" : "");
    b.textContent = MODE_LABEL[mode];
    b.disabled = locked;
    b.onclick = () => {
      if (locked) return;
      if (on) {
        if (!both) return;
        saveQueueMode(mode === "ranked" ? "unranked_only" : "ranked_only");
      } else {
        saveQueueMode("any");
      }
      renderQueueModes();
      renderStatus();
    };
    if (both) {
      const starred = queueMode === (mode === "ranked" ? "ranked_preferred" : "unranked_preferred");
      const star = document.createElement("button");
      star.type = "button";
      star.className = "region-star" + (starred ? " on" : "");
      star.textContent = starred ? "★" : "☆";
      star.setAttribute("aria-pressed", starred ? "true" : "false");
      star.setAttribute("aria-label", mode === "ranked"
        ? "Prefer Ranked when both players accept both modes"
        : "Prefer Unranked when both players accept both modes");
      star.disabled = locked;
      star.onclick = () => {
        if (locked) return;
        saveQueueMode(starred
          ? "any"
          : (mode === "ranked" ? "ranked_preferred" : "unranked_preferred"));
        renderQueueModes();
        renderStatus();
      };
      const slot = document.createElement("span");
      slot.className = "region-slot";
      slot.append(b, star);
      box.appendChild(slot);
      continue;
    }
    box.appendChild(b);
  }
}

function queueModeLabel(mode) {
  if (mode === "any") return "Ranked or Unranked";
  return mode === "unranked_only" || mode === "unranked_preferred" ? "Unranked" : "Ranked";
}

function pendingModeLabel(p) {
  if (!p) return "";
  if (p.mismatch) return "Unfair";
  if (p.rated === false) return "Unranked";
  return "Ranked";
}

// Tell the tray toggle whether this window is mapped. Wails has no visibility
// query, and the Page Visibility API follows the GTK widget's mapped state, so
// this stays correct after a native close-to-tray hide.
const reportVisibility = () => {
  try {
    if (svc.SetWindowVisible) svc.SetWindowVisible(document.visibilityState === "visible");
  } catch (_) {}
};
document.addEventListener("visibilitychange", reportVisibility);
setInterval(reportVisibility, 1000);
reportVisibility();

function renderRace() {
  for (const b of document.querySelectorAll(".race")) {
    b.classList.toggle("on", b.dataset.race === race);
    b.disabled = state.kind !== "idle";
  }
  localStorage.setItem("sl.race", race);
}

function fmt(sec) {
  const s = Math.max(0, Math.floor(sec));
  return String(Math.floor(s / 60)).padStart(2, "0") + ":" + String(s % 60).padStart(2, "0");
}

// The queue lock is a deadline, so it renders from a local clock and the
// 2s status poll only resets the deadline. The Find button is disabled while
// it runs: the server would refuse the join anyway.
function updateLock() {
  const chip = $("lockChip");
  const left = Math.max(0, Math.ceil((state.lockUntil - Date.now()) / 1000));
  if (left > 0) {
    $("lockLabel").textContent = state.lockLabel;
    $("lockLeft").textContent = fmt(left);
    chip.classList.remove("hidden");
  } else {
    chip.classList.add("hidden");
  }
  $("findBtn").disabled = selectedRegions().length === 0 || left > 0;
}

function tick() {
  updateLock();
  if (state.kind !== "searching") return;
  const elapsed = state.elapsed + (Date.now() - state.at) / 1000;
  $("timer").textContent = fmt(elapsed);
}

function rankLabel() {
  const league = leagueFor(race);
  return league ? league.charAt(0).toUpperCase() + league.slice(1) : "Unranked";
}

function statFor(r) {
  return (boot.stats || {})[r] || null;
}

function leagueFor(r) {
  const s = statFor(r);
  return (s && s.league) || (boot.ranks || {})[r] || null;
}

// Paints each race tile with its ladder line: the league emblem, the rating in
// the league color, and a league-colored glow on the selected tile. Unranked
// races keep the neutral tile so a fresh player is not shown a fake number.
function paintRaces() {
  if (!boot.loggedIn) {
    for (const b of document.querySelectorAll(".race")) {
      b.querySelector(".race-bg").classList.add("hidden");
      const v = b.querySelector(".race-rating");
      if (v) v.textContent = "";
    }
    return;
  }
  for (const b of document.querySelectorAll(".race")) {
    const r = b.dataset.race;
    const s = statFor(r);
    const league = leagueFor(r);
    const hex = LEAGUE_HEX[league] || "";
    const bg = b.querySelector(".race-bg");
    const val = b.querySelector(".race-rating");
    if (league) {
      bg.classList.remove("hidden");
      bg.src = "leagues/" + league + ".png";
      val.textContent = s && s.rating ? String(s.rating) : league.charAt(0).toUpperCase() + league.slice(1);
      val.style.color = hex;
    } else {
      bg.classList.add("hidden");
      val.textContent = "Unranked";
      val.style.color = "";
    }
    b.style.setProperty("--race-league", hex || "rgba(108, 184, 255, 0.35)");
  }
}

function bestLeague() {
  let best = null;
  for (const l of Object.values(boot.ranks || {})) {
    if (LEAGUE_ORDER.indexOf(l) > LEAGUE_ORDER.indexOf(best)) best = l;
  }
  return best;
}

const STAR_LEAGUES = ["master", "grandmaster"];

// Paints the league badge exactly as the site builds it: the base rosette,
// then the rank tiers as the player climbs inside the league (top 3 get the
// inner plaques, rank 1 the crown), and the star glow for master and
// grandmaster. The layers carry their own scale, so only the width is set.
function renderMark(badge, blur, mid, top, full, star, league, rank) {
  const starLeague = STAR_LEAGUES.indexOf(league) >= 0;
  badge.src = league ? "leagues/ladder/" + league + "-3.png" : "leagues/unranked.png";
  mid.classList.toggle("hidden", !(league && rank != null && rank <= 3));
  top.classList.toggle("hidden", !(league && rank != null && rank <= 2));
  full.classList.toggle("hidden", !(league && rank === 1));
  blur.classList.toggle("hidden", !(league && starLeague));
  star.classList.toggle("hidden", !(league && starLeague));
  if (!league) return;
  if (rank != null && rank <= 3) mid.src = "leagues/ladder/" + league + "-2.png";
  if (rank != null && rank <= 2) top.src = "leagues/ladder/" + league + "-1.png";
  if (rank === 1) full.src = "leagues/ladder/" + league + "-0.png";
  if (starLeague) {
    blur.src = "leagues/ladder/" + (league === "master" ? "master-star-blur.png" : "grandmaster-starblur.png");
    star.src = "leagues/ladder/" + (league === "master" ? "master-star.png" : "grandmaster-star.png");
  }
}

let mmMatchId = null;
let mmDismissed = "";
let mmAccepting = "";
let mmMismatch = false;
let mmMode = "";
let mmDeadline = 0;
let mmStage = "";
let mmFilledFor = "";

const MM_WINDOW_SECONDS = 60;
const MM_RING_LEN = 2 * Math.PI * 70;

// The deadline is local, so the countdown is drawn from the clock every frame;
// polling only tells us when the stage changes, never where the ring should be.
let mmRaf = null;

function mmFrame() {
  mmRaf = null;
  if (!mmDeadline || mmStage === "ready" || $("mmModal").classList.contains("hidden")) return;
  const leftMs = Math.max(0, mmDeadline - Date.now());
  const left = Math.ceil(leftMs / 1000);
  const pct = Math.max(0, Math.min(100, (leftMs / (MM_WINDOW_SECONDS * 1000)) * 100));
  const timer = $("mmTimer");
  if (timer.dataset.left !== String(left)) {
    timer.dataset.left = String(left);
    timer.setAttribute("aria-label", left + " seconds left");
    $("mmCount").textContent = left;
  }
  $("mmRing").style.strokeDashoffset = (MM_RING_LEN * (1 - pct / 100)).toFixed(1);
  if (leftMs > 0) mmRaf = requestAnimationFrame(mmFrame);
}

function mmTick() {
  if (mmRaf === null) mmRaf = requestAnimationFrame(mmFrame);
}

function flagOf(cc) {
  if (!cc || cc.length !== 2) return "";
  const base = 0x1F1E6;
  return [...cc.toUpperCase()].map((c) => String.fromCodePoint(base + c.charCodeAt(0) - 65)).join("");
}
function raceImg(race) {
  const r = (race || "").toLowerCase();
  if (r !== "terran" && r !== "protoss" && r !== "zerg") return null;
  const img = document.createElement("img");
  img.className = "mm-race";
  img.src = "races/" + r + ".png";
  img.alt = "";
  return img;
}
function leagueImg(league) {
  const l = (league || "").toLowerCase();
  const img = document.createElement("img");
  img.className = "mm-league";
  img.src = "leagues/" + (["bronze", "silver", "gold", "platinum", "diamond", "master", "grandmaster"].includes(l) ? l : "unranked") + ".png";
  img.alt = "";
  return img;
}
function opponentOf(p) {
  if (!p || !p.player_a || !p.player_b) return null;
  return p.player_a.name === boot.name ? p.player_b : p.player_a;
}

// One overlay owns the whole match moment: accept first (no opponent name,
// because nobody has agreed to play yet), then the instructions once both
// sides accepted. Region, map and host are the three facts that decide what
// the players do, so they lead and the steps spell out each click.
function renderMatchModal(p) {
  const modal = $("mmModal");
  const pid = p && (p.id || p.match_id);
  if (!pid) {
    modal.classList.add("hidden");
    mmMatchId = null;
    mmDeadline = 0;
    mmStage = "";
    mmFilledFor = "";
    mmDismissed = "";
    mmAccepting = "";
    mmMismatch = false;
    mmMode = "";
    if (mmRaf !== null) cancelAnimationFrame(mmRaf);
    mmRaf = null;
    return;
  }
  // Close puts the instructions away; the match itself stays alive, so the
  // status poll must not reopen the same card every few seconds.
  if (pid === mmDismissed) {
    if (mmRaf !== null) cancelAnimationFrame(mmRaf);
    mmRaf = null;
    modal.classList.add("hidden");
    return;
  }
  const acc = p.accept || {};
  // While the accept request is in flight a poll can still report the old
  // unconfirmed state; keep the card on Waiting instead of flipping back.
  if (acc.your_confirm) mmAccepting = "";
  const stage = !acc.your_confirm
    ? (pid === mmAccepting ? "waiting" : "accept")
    : (!acc.opponent_confirm ? "waiting" : "ready");
  if (pid !== mmMatchId) {
    mmMatchId = pid;
    $("mmTimer").dataset.left = "";
    const d = acc.deadline ? Date.parse(acc.deadline) : NaN;
    mmDeadline = isNaN(d) ? Date.now() + MM_WINDOW_SECONDS * 1000 : d;
    mmFilledFor = "";
    // A new pairing starts with no map art: the previous match's backdrop is
    // still on the card, and the accept step must not show it before the
    // draft is confirmed.
    $("mmMap").textContent = "";
    $("mmMapCopy").classList.add("hidden");
    $("mmMapImg").removeAttribute("src");
    $("mmMapImg").classList.add("hidden");
    $("mmBg").style.backgroundImage = "";
    $("mmBg").classList.add("hidden");
    state.mapName = "";
  }
  mmStage = stage;
  mmMismatch = !!p.mismatch;
  mmMode = pendingModeLabel(p);
  modal.classList.remove("hidden");
  $("mmAccept").dataset.match = pid;
  $("mmDecline").dataset.match = pid;
  paintStage(stage);
  if (stage !== "ready") return;
  const key = pid + "|" + (acc.map_ready ? "1" : "0");
  if (key !== mmFilledFor) {
    fillReady(p, acc);
    mmFilledFor = key;
  }
}

// Chrome for the three stages lives in one painter so the Accept click can
// move the card to Waiting the moment it is pressed: waiting for the server
// roundtrip and then the 2s poll left the Accept screen up for a second or
// more after a click that had already been counted.
function paintStage(stage) {
  mmStage = stage;
  const modeEyebrow = mmMode === "Unfair"
    ? "Unfair Match Warning"
    : (mmMode ? mmMode + " Match" : "Match found");
  $("mmEyebrow").textContent = stage === "ready" && !mmMode ? "Match ready" : modeEyebrow;
  $("mmTitle").classList.toggle("hidden", stage === "accept");
  $("mmTitle").textContent = stage === "accept"
    ? "Accept match"
    : (stage === "waiting" ? "Waiting for your opponent" : "Your game is ready");
  $("mmCard").classList.toggle("is-mismatch", mmMismatch);
  $("mmAccept").textContent = mmMismatch ? "Play unranked" : "Accept";
  $("mmTimer").classList.toggle("hidden", stage === "ready");
  $("mmCard").classList.toggle("is-timer", stage !== "ready");
  $("mmActions").classList.toggle("off", stage !== "accept");
  if (stage === "accept") {
    // A successful Decline leaves its button disabled for the rest of that
    // match; without this reset the next match's Decline inherits it.
    $("mmAccept").disabled = false;
    $("mmDecline").disabled = false;
  } else {
    const focused = document.activeElement;
    if (focused && $("mmActions").contains(focused)) focused.blur();
  }
  $("mmReady").classList.toggle("hidden", stage !== "ready");
  if (stage !== "ready") mmTick();
}

const REGION_FLAG = { US: "\u{1F1FA}\u{1F1F8}", EU: "\u{1F1EA}\u{1F1FA}", KR: "\u{1F1F0}\u{1F1F7}" };
const REGION_SERVER = { US: "Americas", EU: "Europe", KR: "Korea" };

function fillReady(p, acc) {
  const opp = opponentOf(p);
  const oppName = (opp && opp.name) || p.opponent_name || "your opponent";
  const hostName = (p.host && p.host.name) || "";
  const iHost = !!(hostName && boot.name && hostName === boot.name);
  const map = p.map || {};
  const realMapName = acc.map_name || map.name || "";
  const mapName = realMapName || "the drafted map";
  const code = REGION_LABEL[p.region] || String(p.region || "").toUpperCase();
  const flag = REGION_FLAG[code] || "";
  const server = REGION_SERVER[code] || code || "your region";

  $("mmRegion").textContent = (flag ? flag + " " : "") + (code || "-");
  $("mmMap").textContent = mapName;
  state.mapName = realMapName;
  $("mmMapCopy").classList.toggle("hidden", !realMapName);

  const img = $("mmMapImg");
  const bg = $("mmBg");
  if (map.image_url) {
    const src = map.image_url.startsWith("http") ? map.image_url : SITE + map.image_url;
    img.src = src;
    img.classList.remove("hidden");
    bg.style.backgroundImage = 'url("' + src + '")';
    bg.classList.remove("hidden");
  } else {
    img.removeAttribute("src");
    img.classList.add("hidden");
    bg.style.backgroundImage = "";
    bg.classList.add("hidden");
  }

  const oppRow = $("mmOpp").closest(".mm-fact");
  if (oppRow) oppRow.classList.add("opp");
  $("mmOpp").textContent = oppName;
  const nameFlag = document.createElement("span");
  nameFlag.className = "mm-flag";
  nameFlag.textContent = flagOf(opp && opp.country) || flag || "";
  $("mmOpp").appendChild(nameFlag);
  const oppIsA = !!(p.player_a && opp && p.player_a.user_id === opp.user_id);
  const oppRace = oppIsA ? p.race : p.race_b;
  const oppRating = oppIsA ? p.rating_a : p.rating_b;
  const oppLeague = oppIsA ? p.league_a : p.league_b;

  const fl = $("mmFlavor");
  fl.textContent = "";
  const icon = raceImg(oppRace);
  if (icon) fl.appendChild(icon);
  if (oppRace) {
    const r = document.createElement("span");
    r.textContent = oppRace.charAt(0).toUpperCase() + oppRace.slice(1);
    fl.appendChild(r);
  }
  fl.appendChild(leagueImg(oppLeague));
  const rt = document.createElement("span");
  rt.className = "mm-rating";
  rt.textContent = oppRating == null ? "unranked" : String(oppRating);
  fl.appendChild(rt);

  const h2h = p.head_to_head;
  const h2hEl = $("mmH2H");
  if (h2h && h2h.games > 0) {
    const score = h2h.draws > 0
      ? h2h.wins + " - " + h2h.losses + " - " + h2h.draws
      : h2h.wins + " - " + h2h.losses;
    const pct = h2h.win_rate == null ? "" : " (" + Math.round(h2h.win_rate * 100) + "% WR)";
    h2hEl.textContent = "Record " + score + pct;
    h2hEl.classList.remove("hidden");
  } else {
    h2hEl.textContent = "";
    h2hEl.classList.add("hidden");
  }
  const playedRegions = boot.playedRegions || [];
  const needsChannel = !!(code && !playedRegions.includes(code.toLowerCase()));
  $("mmChan").classList.toggle("hidden", !needsChannel);
  if (needsChannel) $("mmChanRegion").textContent = server;
  $("mmVerdict").textContent = iHost ? "YOU HOST" : "WAIT FOR THE INVITE";
  $("mmVerdict").classList.toggle("join", !iHost);
  $("mmLead").textContent = iHost
    ? "Create the lobby and invite " + oppName + "."
    : (hostName || oppName) + " is creating the lobby.";

  const steps = iHost
    ? [
        "Open StarCraft II on the " + (flag ? flag + " " : "") + server + " server.",
        "Custom \u2192 Melee \u2192 " + mapName + " \u2192 Create with Mod \u2192 StarLance.",
        "Invite " + oppName + ", then press Start.",
      ]
    : [
        "Open StarCraft II on the " + (flag ? flag + " " : "") + server + " server.",
        "Accept the invite from " + (hostName || oppName) + ".",
        "Pick your race and wait for the host to start.",
      ];
  const ol = $("mmSteps");
  ol.textContent = "";
  for (const step of steps) {
    const li = document.createElement("li");
    li.textContent = step;
    ol.appendChild(li);
  }
}

$("mmAccept").onclick = async () => {
  const id = $("mmAccept").dataset.match;
  if (!id) return;
  mmAccepting = id;
  $("mmAccept").blur();
  paintStage("waiting");
  try {
    await svc.AcceptMatch(id);
  } catch (_) {
    mmAccepting = "";
    paintStage("accept");
    noteHoldUntil = Date.now() + 8500;
    showNoteText("Could not accept · try again");
    return;
  }
  refresh().catch(() => {});
};

$("mmDecline").onclick = async () => {
  const id = $("mmDecline").dataset.match;
  if (!id) return;
  $("mmDecline").disabled = true;
  try {
    await svc.DeclineMatch(id);
  } catch (_) {
    $("mmDecline").disabled = false;
    noteHoldUntil = Date.now() + 8500;
    showNoteText("Could not decline · try again");
    return;
  }
  $("mmModal").classList.add("hidden");
  mmDeadline = 0;
  if (mmRaf !== null) cancelAnimationFrame(mmRaf);
  mmRaf = null;
  refresh().catch(() => {});
};

// Leaving a ready match is destructive but reversible by nothing: void it,
// hand the opponent their wait back, and sit out the server's leave lock.
// The first click only arms the button; the second one leaves.
async function leaveMatch(btn, id) {
  if (!id) return;
  const armUntil = Number(btn.dataset.arm || 0);
  if (Date.now() < armUntil) {
    btn.dataset.arm = "";
    btn.disabled = true;
    let lock = 0;
    let failed = false;
    try {
      const r = await svc.LeaveMatch(id);
      lock = (r && r.lock_seconds) || 0;
    } catch (_) {
      failed = true;
    }
    btn.disabled = false;
    btn.textContent = btn.dataset.label || "Leave match";
    btn.className = btn.dataset.base || "btn ghost";
    if (failed) {
      noteHoldUntil = Date.now() + 8500;
      showNoteText("Could not leave the match · try again");
      return;
    }
    mmDismissed = mmMatchId;
    noteHoldUntil = Date.now() + 8500;
    await refresh();
    showLeftNote();
    return;
  }
  btn.dataset.base = btn.className;
  btn.dataset.label = btn.textContent;
  btn.dataset.arm = String(Date.now() + 5000);
  btn.textContent = "Leave match? click again";
  btn.classList.add("danger");
  setTimeout(() => {
    if (Date.now() >= Number(btn.dataset.arm || 0)) {
      btn.dataset.arm = "";
      btn.textContent = btn.dataset.label || "Leave match";
      btn.className = btn.dataset.base || "btn ghost";
    }
  }, 5200);
}

$("leaveBtn").onclick = () => {
  const p = state.pending || {};
  leaveMatch($("leaveBtn"), p.id || p.match_id);
};

$("mmDone").onclick = () => {
  mmDismissed = mmMatchId;
  $("mmModal").classList.add("hidden");
  renderStatus();
};

$("showBtn").onclick = () => {
  mmDismissed = "";
  renderStatus();
};


function renderStatus() {
  const idle = state.kind === "idle";
  const searching = state.kind === "searching";
  // A pending match is a match even if the queue state lags behind it.
  const matched = state.kind === "matched" ||
    !!(state.pending && (state.pending.id || state.pending.match_id));

  const dismissed = matched && (state.pending ? (state.pending.id || state.pending.match_id) : "") === mmDismissed;
  $("findBtn").classList.toggle("hidden", !idle);
  $("hideBtn").classList.toggle("hidden", !searching);
  $("stopBtn").classList.toggle("hidden", !searching);
  $("showBtn").classList.toggle("hidden", !dismissed);
  $("leaveBtn").classList.toggle("hidden", !dismissed);
  $("queuebar").classList.toggle("hidden", matched && !dismissed);
  renderMatchModal(matched ? (state.pending || {}) : null);
  // One owner for the Find button: the lock deadline also disables it, and a
  // second writer here flipped it enabled every poll, flashing the button.
  updateLock();
  const league = leagueFor(race);
  const hex = LEAGUE_HEX[league] || null;
  const bar = $("queuebar");
  bar.dataset.state = state.kind;
  bar.dataset.league = league || "unranked";
  if (hex) {
    bar.style.setProperty("--league", hex);
    bar.style.setProperty("--league-glow", hex + "55");
  } else {
    bar.style.removeProperty("--league");
    bar.style.removeProperty("--league-glow");
  }
  const mark = $("rankMark");
  mark.classList.toggle("hidden", !idle || !boot.loggedIn);
  if (idle && boot.loggedIn) {
    const rank = statFor(race) && statFor(race).rankInLeague;
    renderMark(
      $("rankBadge"), $("rankStarBlur"), $("rankBadge2"), $("rankBadge1"),
      $("rankBadge0"), $("rankStar"), league, rank,
    );
  }
  for (const b of document.querySelectorAll(".region")) b.disabled = !idle;

  $("timer").classList.toggle("hidden", !searching);
  $("timer").textContent = fmt(
    searching ? state.elapsed + (Date.now() - state.at) / 1000 : 0,
  );

  const dot = $("qdot");
  dot.className = "qdot" + (searching ? " searching" : matched ? " matched" : "");
  dot.classList.toggle("hidden", idle);

  const n = state.counters.in_queue || 0;
  const ranked = !!league;
  let text = rankLabel();
  const stats = statFor(race);
  if (idle && stats && stats.rating) {
    if (stats.rankInLeague) text += " #" + stats.rankInLeague;
    text += " · " + stats.rating;
  }
  let muted = !ranked;
  if (searching) {
    const mode = queueModeLabel(state.queuePreference || queueMode);
    const wait = n > 1 ? n + " in queue" : "Waiting for an opponent";
    text = mode + " · " + wait;
    muted = false;
  } else if (matched) {
    const mode = pendingModeLabel(state.pending);
    text = mode ? mode + " match in progress" : "Match in progress...";
    muted = false;
  }
  $("qtext").textContent = text;
  $("qtext").classList.toggle("muted", muted);
  $("qtext").style.color = idle && hex ? hex : "";

  const spark = $("barSpark");
  const trend = idle && boot.loggedIn ? trendPoints(race) : [];
  const drawn = renderTrend(spark, trend, 100, 26) !== null;
  spark.classList.toggle("hidden", !drawn);
  spark.style.color = hex || "var(--muted)";
  spark.style.opacity = "0.85";
  // Social proof before population arrives: distinct players who entered the
  // queue today, whether or not a match resulted. Hidden at zero.
  const queued = idle ? state.counters.queued_today || 0 : 0;
  const queuedEl = $("barToday");
  queuedEl.classList.toggle("hidden", !queued);
  queuedEl.textContent = queued
    ? queued + (queued === 1 ? " player queued today" : " players queued today")
    : "";

  const today = state.counters.matches_today || 0;
  $("today").textContent =
    boot.loggedIn && today > 0 ? (today === 1 ? "1 game today" : today + " games today") : "";

  if (matched && lastKind !== "matched") svc.MatchFound().catch(() => {});
  lastKind = state.kind;

  renderRace();
  renderQueueModes();
}

// A compact season trend for the selected race, drawn into the idle bar where
// the space is otherwise empty. Points are ordered by game, so a dense burst
// never clumps. Returns the season delta in rating points, or null when there
// is no trend to draw.
function trendPoints(race) {
  const s = (boot.ratings || []).find((x) => x.race === race);
  return s && s.points ? s.points : [];
}

function renderTrend(host, points, W, H) {
  host.innerHTML = "";
  if (!points || points.length < 2) return null;
  const P = 2;
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", "0 0 " + W + " " + H);
  const own = points.map((p) => p.rating);
  const lo = Math.min(...own), hi = Math.max(...own);
  const span = hi - lo || 1;
  const xy = points.map((p, i) => {
    const x = P + (i / (points.length - 1)) * (W - 2 * P);
    const y = H - P - ((p.rating - lo) / span) * (H - 2 * P);
    return [x, y];
  });
  const line = document.createElementNS(ns, "polyline");
  line.setAttribute("points", xy.map(([x, y]) => x.toFixed(1) + "," + y.toFixed(1)).join(" "));
  line.setAttribute("fill", "none");
  line.setAttribute("stroke", "currentColor");
  line.setAttribute("stroke-width", "1.5");
  line.setAttribute("stroke-linejoin", "round");
  line.setAttribute("stroke-linecap", "round");
  svg.appendChild(line);
  const [ex, ey] = xy[xy.length - 1];
  const dot = document.createElementNS(ns, "circle");
  dot.setAttribute("cx", ex.toFixed(1));
  dot.setAttribute("cy", ey.toFixed(1));
  dot.setAttribute("r", "2.2");
  dot.setAttribute("fill", "currentColor");
  svg.appendChild(dot);
  host.appendChild(svg);
  return points[points.length - 1].rating - points[0].rating;
}

function renderSettings() {
  $("setName").textContent = boot.loggedIn ? boot.name || "Logged in" : "Not logged in";
  let wins = 0, losses = 0;
  for (const s of Object.values(boot.stats || {})) {
    wins += s.wins || 0;
    losses += s.losses || 0;
  }
  const games = wins + losses;
  $("setRecord").textContent =
    boot.loggedIn && games
      ? wins + "\u2013" + losses + " \u00b7 " + Math.round((100 * wins) / games) + "% win rate"
      : "";
  const accountLeague = bestLeague();
  const accountBadge = $("setBadge");
  accountBadge.classList.toggle("hidden", !boot.loggedIn);
  if (boot.loggedIn) {
    accountBadge.src = accountLeague
      ? "leagues/small-" + accountLeague + ".png"
      : "leagues/unranked.png";
    accountBadge.style.filter = accountLeague
      ? "drop-shadow(0 0 7px " + LEAGUE_HEX[accountLeague] + "66)"
      : "";
  }
  $("authBtn").textContent = boot.loggedIn ? "Log out" : "Log in";
  $("setVersion").textContent = boot.version ? "Version " + boot.version : "";
  const installPath = boot.installPath || "";
  $("setInstall").textContent = installPath
    ? "This copy runs from " + installPath
    : "This copy runs from the folder you downloaded it to.";
  $("setInstall").classList.toggle("hidden", false);
  $("uninstallBtn").disabled = !installPath;
  $("startSystem").checked = !!boot.startWithSystem;
  $("replaysPublic").checked = boot.replays_public !== false;
  $("replaysPublic").disabled = !boot.loggedIn;

  const list = $("folders");
  list.innerHTML = "";
  const dirs = boot.folders || [];
  $("foldersHint").classList.toggle("hidden", dirs.length > 0);
  if (!dirs.length) {
    const li = document.createElement("li");
    li.className = "empty";
    li.textContent = "No folder yet. Press Add folder and pick where StarCraft II saves your replays.";
    list.appendChild(li);
  }
  for (const dir of dirs) {
    const li = document.createElement("li");
    li.className = "path-row";
    li.onclick = async () => {
      try {
        await svc.OpenFolder(dir);
      } catch (e) {
        showSet(String(e && e.message ? e.message : e), true);
      }
    };
    const name = document.createElement("span");
    name.className = "path";
    name.textContent = dir;
    const rm = document.createElement("button");
    rm.className = "x";
    rm.textContent = "Remove";
    rm.onclick = async () => {
      try {
        boot.folders = await svc.RemoveFolder(dir);
        showSet("Folder removed.");
        renderSettings();
        renderPanels();
      } catch (e) {
        showSet(String(e && e.message ? e.message : e), true);
      }
    };
    li.append(name, rm);
    list.appendChild(li);
  }
}

// A decline or an accept timeout puts the innocent side back in queue; say so
// next to the region row, then let the note retire on its own.
let voidTimer = null;
// Status polling must not erase a note the user has not read yet.
let noteHoldUntil = 0;

// The requeued player only needs to know what the opponent did; the codes are
// the backend's void reasons, and a left ready match is not an expiry.
const VOID_NOTES = {
  declined: "The other player declined",
  declined_mismatch: "Unrated match declined",
  left_ready: "The other player left the match",
  accept_timeout: "The other player did not accept",
};
function showVoidNote(reason) {
  showNoteText(VOID_NOTES[reason] || VOID_NOTES.accept_timeout);
}

function showLeftNote() {
  showNoteText("You left the match");
}

function showNoteText(text) {
  const el = $("voidNote");
  if (!el) return;
  el.textContent = text;
  clearTimeout(voidTimer);
  el.style.opacity = "1";
  el.classList.remove("hidden");
  voidTimer = setTimeout(() => {
    el.style.opacity = "0";
    setTimeout(() => el.classList.add("hidden"), 320);
  }, 8000);
}

function hideVoidNote() {
  clearTimeout(voidTimer);
  $("voidNote").classList.add("hidden");
}

async function refresh() {
  if (!svc || !boot.loggedIn) return;
  try {
    const s = await svc.Status();
    const next = s.state || "idle";
    const lv = s.last_void;
    if (next === "searching" && lv && lv.at && lv.at !== state.voidSeenAt) {
      const age = Date.now() - Date.parse(lv.at);
      if (age >= 0 && age < 20000) showVoidNote(lv.reason);
      state.voidSeenAt = lv.at;
    }
    if (next !== "searching" && Date.now() > noteHoldUntil) hideVoidNote();
    if (next === "searching" && state.kind !== "searching") state.at = Date.now();
    state.kind = next;
    state.elapsed = s.elapsed_seconds || 0;
    if (state.kind === "searching") state.at = Date.now();
    state.counters = s.counters || {};
    state.pending = s.pending_match || null;
    state.queuePreference = s.queue_preference || "";
    state.lockUntil = s.locked_until ? Date.parse(s.locked_until) : 0;
    state.lockLabel = (lv && lv.reason === "declined") ? "Decline penalty" : "Leave penalty";
    updateLock();
    // A folder can be added from the tray while this window is open; the
    // status poll carries the current list so the blocked Play tab lifts.
    const dirs = Array.isArray(s.folders) ? s.folders : [];
    if (dirs.join("\n") !== (boot.folders || []).join("\n")) {
      boot.folders = dirs;
      renderSettings();
    }
    // A search can be live from the website while this window shows the
    // blocked card; keep the stop button reachable either way.
    renderPanels();
    showError("");
    renderStatus();
  } catch (e) {
    showError(String(e && e.message ? e.message : e));
  }
}

function schedule() {
  clearTimeout(poll);
  poll = setTimeout(async () => {
    await refresh();
    schedule();
  }, state.kind === "searching" ? 2000 : (state.kind === "matched" ? 1000 : 5000));
}

// The channel is a once-per-install step, so the ladder hint retires by itself
// after the first finished game; Settings keeps a permanent copy row.
function totalGames() {
  let n = 0;
  for (const k in boot.stats || {}) {
    const st = boot.stats[k] || {};
    n += (st.wins || 0) + (st.losses || 0);
  }
  return n;
}

// The hint starts hidden and is only revealed once Bootstrap knows the game
// count, so a returning player never sees it flash before it retires.
function renderChannelHint() {
  $("chanHint").classList.toggle("hidden", totalGames() > 0);
}

function renderWho() {
  renderChannelHint();
  $("player").textContent = boot.loggedIn ? boot.name || "Logged in" : "Not logged in";
  $("dot").classList.toggle("on", !!boot.loggedIn);
}

function noReplayFolder() {
  return !(boot.folders && boot.folders.length);
}

// The Play tab is a checklist: log in, then have a replays folder. Queueing
// stays blocked until both hold, because a game played with no watched folder
// could never be uploaded. The folder card sends the player to Settings
// instead of throwing a modal picker at launch.
function renderPanels() {
  const noFolder = noReplayFolder();
  const busy = !!boot.loggedIn && state.kind && state.kind !== "idle";
  $("folderPanel").classList.toggle("hidden", !noFolder);
  $("loginPanel").classList.toggle("hidden", !!boot.loggedIn);
  $("queuePanel").classList.toggle("hidden", !(boot.loggedIn && (!noFolder || busy)));
}

async function bootUp() {
  if (!svc) {
    showError("This window must be opened from the StarLance App.");
    return;
  }
  let next;
  try {
    next = await svc.Bootstrap();
  } catch (e) {
    // The backend can be down right after a reboot, during a deploy, or
    // while the player is offline. Paint a usable window (login panel plus
    // the reason) and retry, instead of leaving the Play tab blank forever.
    clearTimeout(bootRetry);
    showError(bootOffline(e));
    $("loginPanel").classList.remove("hidden");
    $("queuePanel").classList.add("hidden");
    renderWho();
    renderRegions();
    renderSettings();
    paintRaces();
    renderStatus();
    bootRetry = setTimeout(bootUp, 4000);
    return;
  }
  clearTimeout(bootRetry);
  boot = next;
  showError("");
  if (boot.siteURL) SITE = boot.siteURL;
  $("version").textContent = boot.version || "";
  renderWho();
  renderPanels();
  syncMapBans();
  renderRegions();
  renderSettings();
  paintRaces();
  renderStatus();
  if (boot.loggedIn) await refresh();
  schedule();
}

function bootOffline(e) {
  const msg = String(e && e.message ? e.message : e).trim();
  if (!msg || /cannot reach StarLance/i.test(msg)) {
    return "Can't reach StarLance right now - retrying…";
  }
  return msg.replace(/\.$/, "") + " - retrying…";
}

async function doLogin(btn) {
  const label = btn.textContent;
  btn.disabled = true;
  btn.textContent = "Waiting for browser…";
  showSet("");
  try {
    await svc.Login();
    const wait = setInterval(async () => {
      try {
        boot = await svc.Bootstrap();
      } catch (e) {
        return;
      }
      if (boot.loggedIn) {
        clearInterval(wait);
        btn.disabled = false;
        await bootUp();
      }
    }, 2000);
  } catch (e) {
    const msg = String(e && e.message ? e.message : e);
    showSet(msg, true);
    showError(msg);
    btn.disabled = false;
    btn.textContent = label;
  }
}

// ---------- map bans ----------
// The website owns the same editor; this tab mirrors it inside the window so
// a player who queues from here never leaves the app to change which maps
// they refuse. One pool covers every match, regardless of either side's race.
// It loads lazily the first time the tab is opened and is re-fetched after
// every login.
let mb = { loaded: false, loading: false, pool: [], allowance: 2, bans: [], confirmed: [], saving: [] };
let mbSaveTimer = null;

// No error copy on this tab: a wrong move (banning past the allowance, or a
// failed save) reads as a red shake of the counter and, when there is one, the
// tile that was clicked - never the whole grid.
function shakeEl(el) {
  if (!el) return;
  el.classList.remove("mb-shake");
  void el.offsetWidth;
  el.classList.add("mb-shake");
  clearTimeout(el._mbShakeTimer);
  el._mbShakeTimer = setTimeout(() => el.classList.remove("mb-shake"), 450);
}

function shakeMapBans(tile) {
  shakeEl($("mbCount"));
  if (tile) shakeEl(tile);
}

// The save reports on the map itself: a small ring spins on the tile while the
// change is in flight, and only a confirmed change paints the ban border.
function clearMBFlash() {}

function mbNote(msg) {
  $("mbNote").textContent = msg || "";
  $("mbNote").classList.toggle("hidden", !msg);
}

function syncMapBans() {
  if (!boot.loggedIn) {
    mb.loaded = false;
    mb.loading = false;
    mb.bans = [];
    mb.confirmed = [];
    mb.saving = [];
    mbNote("");
  }
  $("mbLogin").classList.toggle("hidden", !!boot.loggedIn);
  $("mbPanel").classList.toggle("hidden", !boot.loggedIn);
  if (boot.loggedIn && !$("tab-mapbans").classList.contains("hidden")) loadMapBans();
}

async function loadMapBans(force) {
  if (!boot.loggedIn) {
    syncMapBans();
    return;
  }
  if (mb.loaded && !force) {
    renderMapBans();
    return;
  }
  if (mb.loading) return;
  mb.loading = true;
  mbNote("Loading…");
  try {
    const view = await svc.MapBans();
    mb.pool = view.pool || [];
    mb.allowance = view.allowance > 0 ? view.allowance : 2;
    mb.bans = (view.map_ids || []).slice();
    mb.confirmed = mb.bans.slice();
    mb.loaded = true;
    mbNote("");
    renderMapBans();
  } catch (e) {
    mbNote("");
    shakeMapBans();
  } finally {
    mb.loading = false;
  }
}

// A square-ish grid for the pool size: 6 maps paint 3x2, 4 paint one clean
// row, 9 paint a 3x3 - the tile count decides the columns, not a fixed rule.
function mbCols(n) {
  if (n <= 1) return 1;
  if (n === 2) return 2;
  if (n === 3) return 3;
  if (n === 4) return 4;
  if (n <= 6) return 3;
  if (n <= 8) return 4;
  if (n === 9) return 3;
  return 4;
}

// Tiles are built once and then repainted in place: rebuilding the grid on
// every click drops the hovered tile's state for a frame, which reads as the
// border glow flickering off and back on.
function paintMapBans() {
  $("mbCount").textContent = mb.confirmed.length + "/" + mb.allowance + " banned";
  for (const tile of $("mbMaps").querySelectorAll(".mb-tile")) {
    const id = tile.dataset.mapId;
    const at = mb.confirmed.indexOf(id);
    tile.classList.toggle("on", at >= 0);
    const spin = tile.querySelector(".mb-spin");
    const wanted = mb.saving.indexOf(id) >= 0;
    if (wanted && !spin) {
      const el = document.createElement("span");
      el.className = "mb-spin";
      tile.appendChild(el);
    } else if (!wanted && spin) {
      spin.remove();
    }
  }
}

function renderMapBans() {
  const maps = $("mbMaps");
  maps.innerHTML = "";
  if (!mb.pool.length) {
    const p = document.createElement("p");
    p.className = "hint";
    p.textContent = "No maps are in the ladder pool right now, so there is nothing to ban.";
    maps.appendChild(p);
    return;
  }
  maps.style.gridTemplateColumns = "repeat(" + mbCols(mb.pool.length) + ", minmax(0, 1fr))";

  for (const map of mb.pool) {
    const tile = document.createElement("button");
    tile.type = "button";
    tile.className = "mb-tile";
    tile.dataset.mapId = map.id;
    tile.dataset.mapName = map.name;
    tile.onclick = () => toggleBan(map.id, tile);

    if (map.image_url) {
      const img = document.createElement("img");
      img.src = map.image_url.startsWith("http") ? map.image_url : SITE + map.image_url;
      img.alt = "";
      img.loading = "lazy";
      // A missing preview (site unreachable, old map) still leaves a usable
      // tile: the name below stays, the frame shows the placeholder instead.
      img.onerror = () => {
        const ph = document.createElement("span");
        ph.className = "mb-tile-ph";
        ph.textContent = "no preview";
        img.replaceWith(ph);
      };
      tile.appendChild(img);
    } else {
      const ph = document.createElement("span");
      ph.className = "mb-tile-ph";
      ph.textContent = "no preview";
      tile.appendChild(ph);
    }
    const name = document.createElement("span");
    name.className = "mb-tile-name";
    name.textContent = map.name;
    tile.appendChild(name);

    maps.appendChild(tile);
  }
  paintMapBans();
}

function toggleBan(mapID, tile) {
  const list = mb.bans.slice();
  const at = list.indexOf(mapID);
  if (at >= 0) {
    list.splice(at, 1);
  } else if (list.length < mb.allowance) {
    list.push(mapID);
  } else {
    shakeMapBans(tile);
    return;
  }
  mb.bans = list;
  if (mb.saving.indexOf(mapID) < 0) mb.saving.push(mapID);
  paintMapBans();
  queueSaveMapBans();
}

// Auto-save: the pool is a preference, not a form, so every change persists on
// its own. The short delay batches rapid clicks into one PUT (last write wins
// because PUT replaces the whole list).
function queueSaveMapBans() {
  clearTimeout(mbSaveTimer);
  mbSaveTimer = setTimeout(saveMapBans, 350);
}

// A failed save has no tile to blame, so only the counter shakes.
async function saveMapBans() {
  try {
    await svc.SaveMapBans(mb.bans.slice());
    mb.saving = [];
    mb.confirmed = mb.bans.slice();
    paintMapBans();
  } catch (e) {
    mb.saving = [];
    mb.bans = mb.confirmed.slice();
    paintMapBans();
    shakeMapBans();
  }
}

for (const tab of document.querySelectorAll(".tab")) {
  tab.onclick = () => {
    for (const t of document.querySelectorAll(".tab")) t.classList.toggle("on", t === tab);
    const name = tab.dataset.tab;
    for (const pane of ["ladder", "mapbans", "settings"]) {
      $("tab-" + pane).classList.toggle("hidden", pane !== name);
    }
    showSet("");
    if (name === "mapbans") loadMapBans();
  };
}

$("folderBtn").onclick = () => {
  const tab = document.querySelector('.tab[data-tab="settings"]');
  if (tab) tab.click();
  const add = $("addFolder");
  if (add) add.focus();
};

$("mbLoginBtn").onclick = () => {
  const tab = document.querySelector('.tab[data-tab="ladder"]');
  if (tab) tab.click();
};

$("races").onclick = (e) => {
  const b = e.target.closest(".race");
  if (!b || state.kind !== "idle") return;
  race = b.dataset.race;
  renderRace();
  renderStatus();
};

$("findBtn").onclick = async () => {
  showError("");
  try {
    await svc.StartQueue(race, selectedRegions(), selectedPreference(), selectedQueuePreference());
    state.kind = "searching";
    state.at = Date.now();
    state.elapsed = 0;
    renderStatus();
    await refresh();
    schedule();
  } catch (e) {
    showError(String(e && e.message ? e.message : e));
    await refresh();
    schedule();
  }
};

$("hideBtn").onclick = () => svc.HideToTray();

$("stopBtn").onclick = async () => {
  showError("");
  try {
    await svc.StopQueue();
    state.kind = "idle";
    state.elapsed = 0;
    renderStatus();
    await refresh();
    schedule();
  } catch (e) {
    showError(String(e && e.message ? e.message : e));
    await refresh();
    schedule();
  }
};

$("loginBtn").onclick = () => doLogin($("loginBtn"));
$("chanCopy").onclick = () => copyIcon($("chanCopy"), "/join StarLance");
$("mmMapCopy").onclick = () => copyIcon($("mmMapCopy"), state.mapName);
$("mmChanCopy").onclick = () => copyIcon($("mmChanCopy"), "/join StarLance");
$("chanCopySet").onclick = () => copyIcon($("chanCopySet"), "/join StarLance");

$("authBtn").onclick = async () => {
  if (!boot.loggedIn) {
    doLogin($("authBtn"));
    return;
  }
  try {
    await svc.Logout();
    boot.loggedIn = false;
    showSet("Logged out.");
    await bootUp();
  } catch (e) {
    showSet(String(e && e.message ? e.message : e), true);
  }
};

$("addFolder").onclick = async () => {
  try {
    boot.folders = await svc.AddFolder();
    showSet("Folder added.");
    renderSettings();
    renderPanels();
  } catch (e) {
    showSet(String(e && e.message ? e.message : e), true);
  }
};

$("openReplays").onclick = async () => {
  try {
    await svc.OpenReplays();
  } catch (e) {
    showSet(String(e && e.message ? e.message : e), true);
  }
};

$("startSystem").onchange = async () => {
  const on = $("startSystem").checked;
  try {
    await svc.SetStartWithSystem(on);
    boot.startWithSystem = on;
    showSet(on ? "Starts with your computer." : "Does not start with your computer.");
  } catch (e) {
    $("startSystem").checked = !on;
    showSet(String(e && e.message ? e.message : e), true);
  }
};

$("replaysPublic").onchange = async () => {
  const on = $("replaysPublic").checked;
  try {
    await svc.SetReplaysPublic(on);
    boot = await svc.Bootstrap();
    renderSettings();
  } catch (e) {
    $("replaysPublic").checked = !on;
    showSet(String(e && e.message ? e.message : e), true);
  }
};

$("checkUpdate").onclick = () => {
  svc.CheckUpdate();
  showSet("Checking for updates…");
};

$("quitBtn").onclick = () => svc.Quit();

function showUninstallConfirm() {
  $("uninstallWarn").textContent = boot.installPath
    ? "This removes the app, any Start with system entry, and every saved login, setting, and upload history on this PC. Your replays and your StarLance account are not affected."
    : "This removes any Start with system entry and every saved login, setting, and upload history on this PC. If another StarLance App is installed, run Uninstall from that copy instead. The app file stays where you launched it; delete it by hand. Your replays and your StarLance account are not affected.";
  $("uninstallModal").classList.remove("hidden");
  $("uninstallCancel").focus();
}

function hideUninstallConfirm() {
  $("uninstallModal").classList.add("hidden");
}

$("uninstallBtn").onclick = showUninstallConfirm;
$("uninstallCancel").onclick = hideUninstallConfirm;
$("uninstallModal").onclick = (e) => {
  if (e.target === $("uninstallModal")) hideUninstallConfirm();
};
$("uninstallConfirm").onclick = async () => {
  const btn = $("uninstallConfirm");
  btn.disabled = true;
  btn.textContent = "Removing\u2026";
  try {
    await svc.Uninstall();
  } catch (e) {
    hideUninstallConfirm();
    btn.disabled = false;
    btn.textContent = "Uninstall";
    showSet("StarLance App could not remove itself. Quit it, then delete the app file by hand.", true);
  }
};

async function copyToClipboard(text) {
  try {
    if (svc.CopyText) await svc.CopyText(text);
    else await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

// Icon-only variant: swap to a check mark instead of changing text, so the
// button keeps its square footprint next to the map name.
const COPY_ICON = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect width="14" height="14" x="8" y="8" rx="2" ry="2"></rect><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"></path></svg>';
const CHECK_ICON = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"></path></svg>';

async function copyIcon(btn, text) {
  if (!text) return;
  const ok = await copyToClipboard(text);
  btn.classList.toggle("copied", ok);
  btn.innerHTML = ok ? CHECK_ICON : COPY_ICON;
  setTimeout(() => {
    btn.classList.remove("copied");
    btn.innerHTML = COPY_ICON;
  }, 1500);
}





$("discord").onclick = (e) => {
  e.preventDefault();
  openURL(SITE + "/discord");
};

$("railSite").onclick = (e) => {
  e.preventDefault();
  openURL(SITE);
};

setInterval(tick, 250);
setInterval(mmTick, 500);
// The window has no push channel; this poll is the only thing that can turn a
// queued state into a match, an accept into instructions, or a finished game
// back into idle. Two seconds keeps the 60s window honest without load.
setInterval(() => { refresh().catch(() => {}); }, 2000);
bootUp();
