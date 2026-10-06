const REFRESH_MS = 2000;

function formatBytes(n) {
  if (!n || n <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
}

function formatTime(ms) {
  if (!ms) return "-";
  const d = new Date(ms);
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

function statusBadge(status) {
  return `<span class="badge"><span class="status-icon status-${status}"></span>${status.replace("_", " ")}</span>`;
}

async function fetchJSON(url) {
  const res = await fetch(url, { cache: "no-store" });
  if (!res.ok) throw new Error(`${url}: ${res.status}`);
  return res.json();
}

function renderStats(stats) {
  document.getElementById("stat-queued").textContent = stats.queued;
  document.getElementById("stat-processing").textContent = stats.processing;
  document.getElementById("stat-completed").textContent = stats.completed;
  document.getElementById("stat-dead_letter").textContent = stats.dead_letter;

  const saved = Math.max(0, stats.total_original_bytes - stats.total_compressed_bytes);
  document.getElementById("stat-saved").textContent = formatBytes(saved);

  const pct = stats.total_original_bytes > 0 ? (saved / stats.total_original_bytes) * 100 : 0;
  document.getElementById("stat-saved-pct").textContent =
    stats.total_original_bytes > 0 ? `${pct.toFixed(1)}% of ${formatBytes(stats.total_original_bytes)}` : "";
}

function renderActive(active) {
  const tbody = document.getElementById("rows-active");
  if (active.length === 0) {
    tbody.innerHTML = `<tr><td colspan="5" class="empty">Nothing in flight</td></tr>`;
    return;
  }
  tbody.innerHTML = active
    .map(
      (j) => `<tr>
        <td>${statusBadge(j.status)}</td>
        <td>${escapeHTML(j.bucket)}</td>
        <td>${escapeHTML(j.object)}</td>
        <td class="mono">${j.retries}</td>
        <td class="mono">${formatTime(j.updated_at)}</td>
      </tr>`
    )
    .join("");
}

function renderCompleted(done) {
  const tbody = document.getElementById("rows-completed");
  if (done.length === 0) {
    tbody.innerHTML = `<tr><td colspan="6" class="empty">Nothing completed yet</td></tr>`;
    return;
  }
  tbody.innerHTML = done
    .map((j) => {
      const saved = j.original_size - j.compressed_size;
      const pct = j.original_size > 0 ? (saved / j.original_size) * 100 : 0;
      return `<tr>
        <td>${escapeHTML(j.bucket)}</td>
        <td>${escapeHTML(j.object)}</td>
        <td class="mono">${formatBytes(j.original_size)}</td>
        <td class="mono">${formatBytes(j.compressed_size)}</td>
        <td class="mono">${saved > 0 ? pct.toFixed(0) + "%" : "-"}</td>
        <td class="mono">${formatTime(j.updated_at)}</td>
      </tr>`;
    })
    .join("");
}

function renderDead(dead) {
  const tbody = document.getElementById("rows-dead");
  if (dead.length === 0) {
    tbody.innerHTML = `<tr><td colspan="5" class="empty">No failed jobs</td></tr>`;
    return;
  }
  tbody.innerHTML = dead
    .map(
      (j) => `<tr>
        <td>${escapeHTML(j.bucket)}</td>
        <td>${escapeHTML(j.object)}</td>
        <td class="mono">${j.retries}</td>
        <td class="err-cell" title="${escapeHTML(j.error)}">${escapeHTML(j.error)}</td>
        <td class="mono">${formatTime(j.updated_at)}</td>
      </tr>`
    )
    .join("");
}

function setLive(ok) {
  const dot = document.getElementById("live-dot");
  const text = document.getElementById("live-text");
  dot.parentElement.classList.toggle("stale", !ok);
  text.textContent = ok ? "live" : "connection lost";
}

async function tick() {
  try {
    const [stats, queued, processing, completed, dead] = await Promise.all([
      fetchJSON("/api/stats"),
      fetchJSON("/api/jobs?status=queued&limit=100"),
      fetchJSON("/api/jobs?status=processing&limit=100"),
      fetchJSON("/api/jobs?status=completed&limit=50"),
      fetchJSON("/api/jobs?status=dead_letter&limit=50"),
    ]);
    renderStats(stats);
    renderActive([...processing, ...queued]);
    renderCompleted(completed);
    renderDead(dead);
    setLive(true);
  } catch (e) {
    console.error(e);
    setLive(false);
  }
}

tick();
setInterval(tick, REFRESH_MS);
