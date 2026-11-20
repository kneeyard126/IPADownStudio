(() => {
  const state = {
    lang: "zh-CN",
    theme: "system",
    settings: null,
    system: null,
    accounts: [],
    activeProfileId: "",
    searchResults: [],
    purchases: { page: 1, totalCount: 0, apps: [] },
    downloads: [],
    downloadFilter: "all",
    transfer: { active: false, logs: [] },
    qrMode: "page",
    challengeId: "",
    drawer: {
      appId: 0,
      bundleId: "",
      appName: "",
      artworkUrl: "",
      platform: "iphone",
      storefront: "cn",
      versions: [],
      filter: "",
      page: 1,
      pageSize: 20,
      loading: false,
      hydrating: new Set()
    }
  };

  const $ = (id) => document.getElementById(id);

  function t(key, vars = {}) {
    const dict = (window.IPADOWN_I18N && window.IPADOWN_I18N[state.lang]) || window.IPADOWN_I18N["zh-CN"];
    let str = dict[key] || key;
    for (const [k, v] of Object.entries(vars)) {
      str = str.replaceAll(`{${k}}`, String(v));
    }
    return str;
  }

  function applyI18n() {
    document.documentElement.lang = state.lang;
    document.querySelectorAll("[data-i18n]").forEach((el) => {
      const k = el.getAttribute("data-i18n");
      if (k) el.textContent = t(k);
    });
    document.querySelectorAll("[data-i18n-placeholder]").forEach((el) => {
      const k = el.getAttribute("data-i18n-placeholder");
      if (k) el.setAttribute("placeholder", t(k));
    });
    populateStorefrontSelects();
    renderAccountPill();
    renderSearchResults();
    renderPurchases();
    renderDownloads();
    renderTransfer();
    renderAccounts();
    renderDiagnostics();
    renderDrawerTable();
  }

  function applyTheme(theme) {
    state.theme = theme || "system";
    let effective = state.theme;
    if (effective === "system") {
      effective = window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
    }
    document.documentElement.setAttribute("data-theme", effective);
  }

  function showToast(msg, kind = "info") {
    const box = $("toast-container");
    const div = document.createElement("div");
    div.className = `toast ${kind}`;
    div.textContent = msg;
    box.appendChild(div);
    setTimeout(() => div.remove(), 4200);
  }

  async function api(url, opts = {}) {
    const res = await fetch(url, {
      headers: { "Content-Type": "application/json", ...(opts.headers || {}) },
      ...opts
    });
    const text = await res.text();
    let data = {};
    try {
      data = text ? JSON.parse(text) : {};
    } catch {
      data = { error: text };
    }
    if (!res.ok) {
      const err = new Error(data.error || `HTTP ${res.status}`);
      err.kind = data.errorKind || "";
      throw err;
    }
    return data;
  }

  function formatBytes(bytes) {
    const n = Number(bytes || 0);
    if (!n || n <= 0) return "-";
    if (n >= 1073741824) return (n / 1073741824).toFixed(2) + " GB";
    if (n >= 1048576) return (n / 1048576).toFixed(1) + " MB";
    if (n >= 1024) return (n / 1024).toFixed(0) + " KB";
    return n + " B";
  }

  function formatSpeed(bps) {
    if (!bps || bps <= 0) return "";
    if (bps >= 1048576) return (bps / 1048576).toFixed(2) + " MB/s";
    if (bps >= 1024) return (bps / 1024).toFixed(0) + " KB/s";
    return Math.round(bps) + " B/s";
  }

  function formatETA(sec) {
    if (!sec || sec <= 0) return "";
    const m = Math.floor(sec / 60);
    const s = Math.floor(sec % 60);
    return `${m}:${String(s).padStart(2, "0")}`;
  }

  function formatDate(iso) {
    if (!iso) return "-";
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso.slice(0, 10);
    return d.toISOString().slice(0, 10);
  }

  function esc(str) {
    return String(str ?? "")
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;");
  }

  function switchView(viewName) {
    document.querySelectorAll(".nav-item").forEach((b) => {
      b.classList.toggle("active", b.dataset.view === viewName);
    });
    document.querySelectorAll(".view").forEach((sec) => {
      sec.classList.toggle("active", sec.id === `view-${viewName}`);
    });
    if (viewName === "transfer") {
      loadTransferInterfaces();
      renderTransfer();
    } else if (viewName === "purchased" && state.purchases.apps.length === 0 && state.activeProfileId) {
      loadPurchases(1);
    }
  }

  function populateStorefrontSelects() {
    const list = (state.system && state.system.storefronts) || [
      { code: "cn", nameZh: "中国大陆 (CN)", nameEn: "China Mainland (CN)" },
      { code: "us", nameZh: "美国 (US)", nameEn: "United States (US)" },
      { code: "jp", nameZh: "日本 (JP)", nameEn: "Japan (JP)" },
      { code: "hk", nameZh: "中国香港 (HK)", nameEn: "Hong Kong (HK)" }
    ];
    ["search-country", "purchases-country", "set-storefront", "login-region"].forEach((id) => {
      const sel = $(id);
      if (!sel) return;
      const prev = sel.value;
      sel.innerHTML = "";
      if (id === "login-region") {
        const opt = document.createElement("option");
        opt.value = "";
        opt.textContent = state.lang === "zh-CN" ? "不指定" : "Optional";
        sel.appendChild(opt);
      }
      for (const sf of list) {
        const opt = document.createElement("option");
        opt.value = sf.code;
        opt.textContent = state.lang === "zh-CN" ? sf.nameZh : sf.nameEn;
        sel.appendChild(opt);
      }
      if (prev) sel.value = prev;
    });
    if (state.settings) {
      if (!$("search-country").value) $("search-country").value = state.settings.defaultStorefront;
      if (!$("purchases-country").value) $("purchases-country").value = state.settings.defaultStorefront;
      $("set-storefront").value = state.settings.defaultStorefront;
    }
  }

  function renderAccountPill() {
    const active = state.accounts.find((a) => a.profileId === state.activeProfileId);
    const avatar = $("sidebar-account-avatar");
    const nameEl = $("sidebar-account-name");
    const subEl = $("sidebar-account-sub");
    const dot = $("sidebar-account-dot");
    if (!active) {
      avatar.textContent = "?";
      nameEl.textContent = t("noAccountPill");
      subEl.textContent = t("clickToSignIn");
      dot.className = "status-dot";
      return;
    }
    const label = active.name || active.email;
    avatar.textContent = (label[0] || "A").toUpperCase();
    nameEl.textContent = label;
    subEl.textContent = active.email;
    dot.className = `status-dot ${active.status === "valid" ? "valid" : active.status === "expired" ? "expired" : ""}`;
  }

  async function refreshBootstrap() {
    const [sys, cfg, accs, dls, tr] = await Promise.all([
      api("/api/system/status"),
      api("/api/settings"),
      api("/api/accounts"),
      api("/api/downloads"),
      api("/api/transfer/status")
    ]);
    state.system = sys;
    state.settings = cfg;
    state.lang = cfg.language || "zh-CN";
    state.accounts = accs.accounts || [];
    state.activeProfileId = accs.activeProfileId || "";
    state.downloads = dls.tasks || [];
    state.transfer = tr || { active: false, logs: [] };

    applyTheme(cfg.theme);
    populateStorefrontSelects();

    $("search-country").value = cfg.defaultStorefront;
    $("search-platform").value = cfg.defaultPlatform;
    $("purchases-country").value = cfg.defaultStorefront;
    $("purchases-platform").value = cfg.defaultPlatform;

    $("set-language").value = cfg.language;
    $("set-theme").value = cfg.theme;
    $("set-storefront").value = cfg.defaultStorefront;
    $("set-platform").value = cfg.defaultPlatform;
    $("set-download-dir").value = cfg.downloadDir || "";
    $("set-concurrency").value = String(cfg.maxConcurrentDownloads || 1);
    $("set-auto-purchase").checked = !!cfg.autoPurchaseFree;
    $("set-custom-ipatool").value = cfg.customIpatoolPath || "";

    applyI18n();
    updateDownloadsBadge();
  }

  function updateDownloadsBadge() {
    const activeCount = state.downloads.filter((d) => d.status === "queued" || d.status === "downloading" || d.status === "verifying").length;
    const badge = $("nav-downloads-badge");
    badge.textContent = String(activeCount);
    badge.classList.toggle("hidden", activeCount === 0);
  }

  // --- Search ---
  async function handleSearchSubmit(e) {
    e.preventDefault();
    const q = $("search-input").value.trim();
    if (!q) return;
    const country = $("search-country").value;
    const platform = $("search-platform").value;
    const btn = $("search-submit-btn");
    btn.disabled = true;
    try {
      const data = await api(`/api/search?q=${encodeURIComponent(q)}&country=${encodeURIComponent(country)}&platform=${encodeURIComponent(platform)}&limit=30`);
      state.searchResults = data.results || [];
      if (data.country && $("search-country").querySelector(`option[value="${data.country}"]`)) {
        $("search-country").value = data.country;
      }
      renderSearchResults(true);
    } catch (err) {
      showToast(err.message, "error");
    } finally {
      btn.disabled = false;
    }
  }

  function renderSearchResults(searched = false) {
    const container = $("search-results");
    if (!state.searchResults.length) {
      container.innerHTML = `<div class="empty-state"><h3>${esc(searched ? t("searchNoResult") : t("searchEmptyTitle"))}</h3><p>${esc(searched ? "" : t("searchEmptyDesc"))}</p></div>`;
      return;
    }
    const platform = $("search-platform").value;
    const country = $("search-country").value;
    container.innerHTML = state.searchResults.map((app) => {
      const priceText = app.price > 0 ? (app.formattedPrice || `$${app.price}`) : t("freeLabel");
      return `
        <div class="app-card">
          <div class="app-card-top">
            <img class="app-icon" src="${esc(app.artworkUrl)}" alt="">
            <div class="app-info">
              <div class="app-name" title="${esc(app.trackName)}">${esc(app.trackName)}</div>
              <div class="app-dev">${esc(app.artistName)}</div>
              <div class="app-meta-tags">
                <span class="tag price">${esc(priceText)}</span>
                ${app.version ? `<span class="tag">v${esc(app.version)}</span>` : ""}
                ${app.fileSizeBytes ? `<span class="tag">${esc(formatBytes(app.fileSizeBytes))}</span>` : ""}
                ${app.minimumOsVersion ? `<span class="tag">${esc(t("minIosPrefix") + app.minimumOsVersion)}</span>` : ""}
              </div>
            </div>
          </div>
          <div class="muted-sm mono">${esc(app.bundleId)} · ID: ${app.trackId}</div>
          <div class="app-card-actions">
            <button class="btn btn-secondary" data-action="versions" data-id="${app.trackId}">${esc(t("viewVersions"))}</button>
            <button class="btn btn-primary" data-action="download-latest" data-id="${app.trackId}">${esc(t("downloadLatest"))}</button>
          </div>
        </div>
      `;
    }).join("");

    container.querySelectorAll("button[data-action]").forEach((btn) => {
      const id = Number(btn.dataset.id);
      const app = state.searchResults.find((a) => a.trackId === id);
      if (!app) return;
      btn.onclick = () => {
        if (btn.dataset.action === "versions") {
          openVersionDrawer({
            appId: app.trackId,
            bundleId: app.bundleId,
            appName: app.trackName,
            artworkUrl: app.artworkUrl,
            platform,
            storefront: country
          });
        } else {
          enqueueDownload({
            appId: app.trackId,
            bundleId: app.bundleId,
            appName: app.trackName,
            artworkUrl: app.artworkUrl,
            platform,
            storefront: country,
            externalVersionId: "",
            displayVersion: app.version || "",
            expectedSizeBytes: app.fileSizeBytes || 0
          });
        }
      };
    });
  }

  function handleDirectVersionsClick() {
    const q = $("search-input").value.trim();
    if (!q) {
      showToast(t("searchPlaceholder"), "warning");
      return;
    }
    const platform = $("search-platform").value;
    const country = $("search-country").value;
    const digitsMatch = q.match(/(?:^|\bid)(\d{5,15})\b/);
    if (digitsMatch) {
      openVersionDrawer({
        appId: Number(digitsMatch[1]),
        bundleId: "",
        appName: `App ${digitsMatch[1]}`,
        artworkUrl: "",
        platform,
        storefront: country
      });
      return;
    }
    if (/^[a-zA-Z0-9-]+(\.[a-zA-Z0-9_-]+)+$/.test(q)) {
      openVersionDrawer({
        appId: 0,
        bundleId: q,
        appName: q,
        artworkUrl: "",
        platform,
        storefront: country
      });
      return;
    }
    $("search-form").requestSubmit();
  }

  // --- Purchases ---
  async function loadPurchases(page = 1) {
    if (!state.activeProfileId) {
      state.purchases = { page: 1, totalCount: 0, apps: [] };
      renderPurchases();
      return;
    }
    const platform = $("purchases-platform").value;
    const country = $("purchases-country").value;
    const btn = $("purchases-refresh-btn");
    btn.disabled = true;
    try {
      const data = await api(`/api/purchases?page=${page}&limit=20&platform=${encodeURIComponent(platform)}&country=${encodeURIComponent(country)}`);
      state.purchases = {
        page: data.page || page,
        totalCount: data.totalCount || 0,
        apps: data.apps || []
      };
      renderPurchases();
    } catch (err) {
      showToast(err.message, "error");
    } finally {
      btn.disabled = false;
    }
  }

  function renderPurchases() {
    const container = $("purchases-list");
    const apps = state.purchases.apps || [];
    $("purchases-page-label").textContent = `${t("pageInfo", { page: state.purchases.page })} · ${t("totalItems", { count: state.purchases.totalCount })}`;
    $("purchases-prev").disabled = state.purchases.page <= 1;
    $("purchases-next").disabled = apps.length < 20;

    if (!apps.length) {
      container.innerHTML = `<div class="empty-state"><p>${esc(t("purchasedEmpty"))}</p></div>`;
      return;
    }
    const platform = $("purchases-platform").value;
    const country = $("purchases-country").value;
    container.innerHTML = apps.map((app) => `
      <div class="app-card">
        <div class="app-card-top">
          <img class="app-icon" src="${esc(app.artworkUrl || "")}" alt="">
          <div class="app-info">
            <div class="app-name" title="${esc(app.name)}">${esc(app.name || app.bundleId)}</div>
            <div class="app-dev">${esc(app.artistName || app.bundleId)}</div>
            <div class="app-meta-tags">
              ${app.version ? `<span class="tag">v${esc(app.version)}</span>` : ""}
              ${app.fileSizeBytes ? `<span class="tag">${esc(formatBytes(app.fileSizeBytes))}</span>` : ""}
              ${app.purchaseDate ? `<span class="tag">${esc(formatDate(app.purchaseDate))}</span>` : ""}
            </div>
          </div>
        </div>
        <div class="muted-sm mono">${esc(app.bundleId)} · ID: ${app.id}</div>
        <div class="app-card-actions">
          <button class="btn btn-secondary" data-paction="versions" data-id="${app.id}">${esc(t("viewVersions"))}</button>
          <button class="btn btn-primary" data-paction="download" data-id="${app.id}">${esc(t("downloadLatest"))}</button>
        </div>
      </div>
    `).join("");

    container.querySelectorAll("button[data-paction]").forEach((btn) => {
      const id = Number(btn.dataset.id);
      const app = apps.find((a) => a.id === id);
      if (!app) return;
      btn.onclick = () => {
        if (btn.dataset.paction === "versions") {
          openVersionDrawer({
            appId: app.id,
            bundleId: app.bundleId,
            appName: app.name || app.bundleId,
            artworkUrl: app.artworkUrl || "",
            platform,
            storefront: country
          });
        } else {
          enqueueDownload({
            appId: app.id,
            bundleId: app.bundleId,
            appName: app.name || app.bundleId,
            artworkUrl: app.artworkUrl || "",
            platform,
            storefront: country,
            externalVersionId: "",
            displayVersion: app.version || "",
            expectedSizeBytes: app.fileSizeBytes || 0
          });
        }
      };
    });
  }

  // --- Version History Drawer ---
  async function openVersionDrawer(info) {
    if (!state.activeProfileId) {
      showToast(t("errNeedLogin"), "warning");
      openLoginModal();
      return;
    }
    state.drawer = {
      ...state.drawer,
      appId: info.appId || 0,
      bundleId: info.bundleId || "",
      appName: info.appName || info.bundleId || `App ${info.appId}`,
      artworkUrl: info.artworkUrl || "",
      platform: info.platform || "iphone",
      storefront: info.storefront || "cn",
      versions: [],
      filter: "",
      page: 1,
      loading: true,
      hydrating: new Set()
    };
    $("drawer-app-name").textContent = state.drawer.appName;
    $("drawer-app-sub").textContent = `${state.drawer.bundleId || "-"} · ID: ${state.drawer.appId || "-"}`;
    $("drawer-app-icon").src = state.drawer.artworkUrl || "";
    $("drawer-app-icon").classList.toggle("hidden", !state.drawer.artworkUrl);
    $("drawer-filter-input").value = "";
    $("drawer-manual-id").value = "";
    $("drawer-corefp-banner").classList.toggle("hidden", !!(state.system && state.system.coreFpReady));
    $("version-drawer-backdrop").classList.remove("hidden");
    renderDrawerTable();

    try {
      const qs = new URLSearchParams({
        bundleId: state.drawer.bundleId,
        platform: state.drawer.platform
      });
      const res = await api(`/api/apps/${state.drawer.appId}/versions?${qs.toString()}`);
      state.drawer.appId = res.appId || state.drawer.appId;
      state.drawer.bundleId = res.bundleId || state.drawer.bundleId;
      state.drawer.versions = res.versions || [];
      state.drawer.loading = false;
      $("drawer-app-sub").textContent = `${state.drawer.bundleId || "-"} · ID: ${state.drawer.appId || "-"}`;
      $("drawer-corefp-banner").classList.toggle("hidden", !!res.coreFpReady);
      if (state.system) state.system.coreFpReady = !!res.coreFpReady;
      renderDrawerTable();
      hydrateVisiblePage();
    } catch (err) {
      state.drawer.loading = false;
      renderDrawerTable(err.message);
    }
  }

  function filteredDrawerVersions() {
    const q = state.drawer.filter.trim().toLowerCase();
    if (!q) return state.drawer.versions;
    return state.drawer.versions.filter((v) =>
      String(v.externalVersionId).toLowerCase().includes(q) ||
      String(v.displayVersion || "").toLowerCase().includes(q)
    );
  }

  function renderDrawerTable(errMsg = "") {
    const tbody = $("drawer-table-body");
    if (state.drawer.loading) {
      tbody.innerHTML = `<tr><td colspan="7" style="text-align:center;padding:36px">${esc(t("drawerLoadingVersions"))}</td></tr>`;
      $("drawer-total-label").textContent = "";
      return;
    }
    if (errMsg) {
      tbody.innerHTML = `<tr><td colspan="7" style="text-align:center;padding:36px;color:var(--danger)">${esc(errMsg)}</td></tr>`;
      return;
    }
    const items = filteredDrawerVersions();
    const totalPages = Math.max(1, Math.ceil(items.length / state.drawer.pageSize));
    if (state.drawer.page > totalPages) state.drawer.page = totalPages;
    const start = (state.drawer.page - 1) * state.drawer.pageSize;
    const slice = items.slice(start, start + state.drawer.pageSize);

    $("drawer-total-label").textContent = t("drawerTotalVersions", { count: items.length });
    $("drawer-page-label").textContent = `${state.drawer.page} / ${totalPages}`;
    $("drawer-prev-page").disabled = state.drawer.page <= 1;
    $("drawer-next-page").disabled = state.drawer.page >= totalPages;

    if (!slice.length) {
      tbody.innerHTML = `<tr><td colspan="7" style="text-align:center;padding:28px;color:var(--text-muted)">-</td></tr>`;
      return;
    }

    tbody.innerHTML = slice.map((v, idx) => {
      const rowNum = start + idx + 1;
      const isHydrating = state.drawer.hydrating.has(v.externalVersionId);
      const verCell = v.displayVersion
        ? `<strong>${esc(v.displayVersion)}</strong>`
        : isHydrating
          ? `<span class="muted-sm">${esc(t("drawerLoadingMeta"))}</span>`
          : `<span class="muted-sm">-</span>`;
      const dateCell = v.releaseDate
        ? `${esc(formatDate(v.releaseDate))}${v.releaseDateEstimated ? ` <span class="tag">${esc(t("drawerEstBadge"))}</span>` : ""}`
        : "-";
      return `
        <tr>
          <td>${rowNum}</td>
          <td class="mono">
            ${esc(v.externalVersionId)}
            ${v.isLatest ? `<span class="tag price">${esc(t("drawerLatestBadge"))}</span>` : ""}
          </td>
          <td>${verCell}</td>
          <td>${dateCell}</td>
          <td>${v.sizeBytes ? esc(formatBytes(v.sizeBytes)) : "-"}</td>
          <td>${v.minimumOsVersion ? esc(v.minimumOsVersion) : "-"}</td>
          <td>
            <div class="row-gap">
              ${!v.displayVersion ? `<button class="btn btn-secondary btn-sm" data-vaction="meta" data-vid="${esc(v.externalVersionId)}">${esc(t("drawerLoadMetaBtn"))}</button>` : ""}
              <button class="btn btn-primary btn-sm" data-vaction="dl" data-vid="${esc(v.externalVersionId)}">${esc(t("drawerDownloadVerBtn"))}</button>
            </div>
          </td>
        </tr>
      `;
    }).join("");

    tbody.querySelectorAll("button[data-vaction]").forEach((btn) => {
      const vid = btn.dataset.vid;
      const entry = state.drawer.versions.find((x) => x.externalVersionId === vid);
      btn.onclick = () => {
        if (btn.dataset.vaction === "meta") {
          hydrateVersionIDs([vid], true);
        } else {
          enqueueDownload({
            appId: state.drawer.appId,
            bundleId: state.drawer.bundleId,
            appName: state.drawer.appName,
            artworkUrl: state.drawer.artworkUrl,
            platform: state.drawer.platform,
            storefront: state.drawer.storefront,
            externalVersionId: vid,
            displayVersion: (entry && entry.displayVersion) || "",
            expectedSizeBytes: (entry && entry.sizeBytes) || 0
          });
        }
      };
    });
  }

  function hydrateVisiblePage() {
    const items = filteredDrawerVersions();
    const start = (state.drawer.page - 1) * state.drawer.pageSize;
    const slice = items.slice(start, start + state.drawer.pageSize);
    const missing = slice
      .filter((v) => !v.cached && !v.displayVersion && !v.error && !state.drawer.hydrating.has(v.externalVersionId))
      .map((v) => v.externalVersionId);
    if (missing.length > 0) {
      hydrateVersionIDs(missing, false);
    }
  }

  async function hydrateVersionIDs(ids, force) {
    ids.forEach((id) => state.drawer.hydrating.add(id));
    renderDrawerTable();
    try {
      const res = await api(`/api/apps/${state.drawer.appId}/versions/metadata`, {
        method: "POST",
        body: JSON.stringify({
          bundleId: state.drawer.bundleId,
          platform: state.drawer.platform,
          externalVersionIds: ids,
          force
        })
      });
      for (const updated of res.versions || []) {
        state.drawer.hydrating.delete(updated.externalVersionId);
        const idx = state.drawer.versions.findIndex((v) => v.externalVersionId === updated.externalVersionId);
        if (idx !== -1) {
          state.drawer.versions[idx] = { ...state.drawer.versions[idx], ...updated };
        }
      }
    } catch (err) {
      ids.forEach((id) => state.drawer.hydrating.delete(id));
      showToast(err.message, "error");
    }
    renderDrawerTable();
  }

  async function acquireDrawerLicense() {
    const btn = $("drawer-purchase-btn");
    btn.disabled = true;
    try {
      const res = await api(`/api/apps/${state.drawer.appId}/purchase`, {
        method: "POST",
        body: JSON.stringify({
          bundleId: state.drawer.bundleId,
          platform: state.drawer.platform
        })
      });
      showToast(res.alreadyOwned ? t("toastAlreadyOwned") : t("toastPurchased"), "success");
    } catch (err) {
      showToast(err.message, "error");
    } finally {
      btn.disabled = false;
    }
  }

  // --- Downloads ---
  async function enqueueDownload(payload) {
    if (!state.activeProfileId) {
      showToast(t("errNeedLogin"), "warning");
      openLoginModal();
      return;
    }
    try {
      const task = await api("/api/downloads", {
        method: "POST",
        body: JSON.stringify(payload)
      });
      upsertDownloadTask(task);
      showToast(t("toastEnqueued"), "success");
    } catch (err) {
      showToast(err.message, "error");
    }
  }

  function upsertDownloadTask(task) {
    const idx = state.downloads.findIndex((d) => d.id === task.id);
    if (idx !== -1) {
      state.downloads[idx] = task;
    } else {
      state.downloads.unshift(task);
    }
    updateDownloadsBadge();
    renderDownloads();
    if (task.status === "completed") {
      populateCompletedTransferSelect();
    }
  }

  function renderDownloads() {
    const container = $("downloads-list");
    const filtered = state.downloads.filter((d) => {
      if (state.downloadFilter === "active") return d.status === "queued" || d.status === "downloading" || d.status === "verifying";
      if (state.downloadFilter === "completed") return d.status === "completed";
      if (state.downloadFilter === "other") return d.status === "paused" || d.status === "failed" || d.status === "cancelled";
      return true;
    });
    if (!filtered.length) {
      container.innerHTML = `<div class="empty-state"><p>${esc(t("downloadsEmpty"))}</p></div>`;
      return;
    }

    const statusLabelMap = {
      queued: "statusQueued",
      downloading: "statusDownloading",
      verifying: "statusVerifying",
      paused: "statusPaused",
      completed: "statusCompleted",
      failed: "statusFailed",
      cancelled: "statusCancelled"
    };

    container.innerHTML = filtered.map((d) => {
      const pct = d.totalBytes > 0 ? Math.min(100, Math.round((d.downloadedBytes / d.totalBytes) * 100)) : (d.status === "completed" ? 100 : 0);
      const verLabel = d.displayVersion ? `v${d.displayVersion}` : (d.externalVersionId || "latest");
      const speedText = d.status === "downloading" ? formatSpeed(d.speedBytesPerSec) : "";
      const etaText = d.status === "downloading" && d.etaSeconds > 0 ? `ETA ${formatETA(d.etaSeconds)}` : "";

      let actions = "";
      if (d.status === "downloading" || d.status === "queued") {
        actions += `<button class="btn btn-secondary btn-sm" data-daction="pause" data-id="${d.id}">${esc(t("btnPause"))}</button>`;
        actions += `<button class="btn btn-secondary btn-sm" data-daction="cancel" data-id="${d.id}">${esc(t("btnCancel"))}</button>`;
      } else if (d.status === "paused") {
        actions += `<button class="btn btn-primary btn-sm" data-daction="resume" data-id="${d.id}">${esc(t("btnResume"))}</button>`;
        actions += `<button class="btn btn-secondary btn-sm" data-daction="cancel" data-id="${d.id}">${esc(t("btnCancel"))}</button>`;
      } else if (d.status === "failed" || d.status === "cancelled") {
        actions += `<button class="btn btn-primary btn-sm" data-daction="resume" data-id="${d.id}">${esc(t("btnRetry"))}</button>`;
      } else if (d.status === "completed") {
        actions += `<button class="btn btn-secondary btn-sm" data-daction="reveal" data-id="${d.id}">${esc(t("btnReveal"))}</button>`;
        actions += `<button class="btn btn-primary btn-sm" data-daction="transfer" data-id="${d.id}">${esc(t("btnSendToPhone"))}</button>`;
      }
      actions += `<button class="btn btn-ghost btn-sm" data-daction="delete" data-id="${d.id}">${esc(t("btnDelete"))}</button>`;

      return `
        <div class="task-card">
          <div class="task-top">
            ${d.artworkUrl ? `<img class="app-icon-sm" src="${esc(d.artworkUrl)}" alt="">` : ""}
            <div class="task-main">
              <div class="task-title-row">
                <span class="task-title">${esc(d.appName)}</span>
                <span class="tag">${esc(verLabel)}</span>
                ${d.externalVersionId ? `<span class="tag mono">${esc(d.externalVersionId)}</span>` : ""}
                <span class="status-badge ${esc(d.status)}">${esc(t(statusLabelMap[d.status] || d.status))}</span>
              </div>
              <div class="muted-sm mono">${esc(d.targetPath)}</div>
            </div>
            <div class="task-actions">${actions}</div>
          </div>
          <div class="progress-wrap">
            <div class="progress-bar"><div class="progress-fill ${esc(d.status)}" style="width:${pct}%"></div></div>
            <div class="progress-stats">
              <span>${esc(formatBytes(d.downloadedBytes))} / ${esc(d.totalBytes > 0 ? formatBytes(d.totalBytes) : "-")} (${pct}%)</span>
              <span>${esc([speedText, etaText, d.accountEmail].filter(Boolean).join(" · "))}</span>
            </div>
          </div>
          ${d.requiresLicense ? `
            <div class="license-alert">
              <span>${esc(t("licenseNotice"))}</span>
              <button class="btn btn-primary btn-sm" data-daction="purchase-retry" data-id="${d.id}">${esc(t("btnAcquireAndRetry"))}</button>
            </div>
          ` : d.errorMessage ? `<div class="modal-error">${esc(d.errorMessage)}</div>` : ""}
        </div>
      `;
    }).join("");

    container.querySelectorAll("button[data-daction]").forEach((btn) => {
      const id = btn.dataset.id;
      const task = state.downloads.find((x) => x.id === id);
      if (!task) return;
      btn.onclick = async () => {
        try {
          switch (btn.dataset.daction) {
            case "pause":
              upsertDownloadTask(await api(`/api/downloads/${id}/pause`, { method: "POST" }));
              break;
            case "resume":
              upsertDownloadTask(await api(`/api/downloads/${id}/resume`, { method: "POST", body: "{}" }));
              break;
            case "purchase-retry":
              upsertDownloadTask(await api(`/api/downloads/${id}/resume`, { method: "POST", body: JSON.stringify({ autoPurchase: true }) }));
              break;
            case "cancel":
              upsertDownloadTask(await api(`/api/downloads/${id}/cancel`, { method: "POST" }));
              break;
            case "reveal":
              await api("/api/system/reveal", { method: "POST", body: JSON.stringify({ path: task.targetPath }) });
              break;
            case "transfer":
              $("transfer-file-path").value = task.targetPath;
              switchView("transfer");
              break;
            case "delete":
              confirmDeleteTask(task);
              break;
          }
        } catch (err) {
          showToast(err.message, "error");
        }
      };
    });
  }

  function confirmDeleteTask(task) {
    $("confirm-modal-msg").textContent = t("confirmDeleteTask");
    const row = $("confirm-delete-file-row");
    const cb = $("confirm-delete-file-cb");
    cb.checked = false;
    row.classList.toggle("hidden", task.status !== "completed");
    $("confirm-modal-backdrop").classList.remove("hidden");

    const close = () => $("confirm-modal-backdrop").classList.add("hidden");
    $("confirm-no-btn").onclick = close;
    $("confirm-yes-btn").onclick = async () => {
      close();
      try {
        await api(`/api/downloads/${task.id}?deleteFile=${cb.checked}`, { method: "DELETE" });
        state.downloads = state.downloads.filter((d) => d.id !== task.id);
        updateDownloadsBadge();
        renderDownloads();
      } catch (err) {
        showToast(err.message, "error");
      }
    };
  }

  // --- Device Transfer ---
  function populateCompletedTransferSelect() {
    const sel = $("transfer-completed-select");
    const completed = state.downloads.filter((d) => d.status === "completed");
    sel.innerHTML = `<option value="">-- ${esc(t("pickFromCompleted"))} --</option>` +
      completed.map((d) => `<option value="${esc(d.targetPath)}">${esc(d.appName)} (${esc(d.displayVersion || d.externalVersionId || "latest")})</option>`).join("");
  }

  async function loadTransferInterfaces() {
    populateCompletedTransferSelect();
    try {
      const data = await api("/api/transfer/interfaces");
      const sel = $("transfer-adapter-select");
      const prev = sel.value;
      const list = data.interfaces || [];
      sel.innerHTML = list.map((it) => `<option value="${esc(it.ip)}">${esc(it.name)} — ${esc(it.ip)}</option>`).join("");
      if (prev && sel.querySelector(`option[value="${prev}"]`)) {
        sel.value = prev;
      }
    } catch (err) {
      showToast(err.message, "error");
    }
  }

  function renderTransfer() {
    const tr = state.transfer || { active: false, logs: [] };
    $("transfer-start-btn").classList.toggle("hidden", !!tr.active);
    $("transfer-stop-btn").classList.toggle("hidden", !tr.active);
    if (tr.active && tr.filePath && !$("transfer-file-path").value) {
      $("transfer-file-path").value = tr.filePath;
    }

    const activeUrl = tr.active ? (state.qrMode === "direct" ? tr.directUrl : tr.pageUrl) : "";
    const activeSvg = tr.active ? (state.qrMode === "direct" ? tr.directQrSvg : tr.pageQrSvg) : "";
    $("transfer-active-url").value = activeUrl || "";
    $("transfer-qr-box").innerHTML = activeSvg || `<div class="qr-placeholder">QR</div>`;
    $("transfer-count-badge").textContent = String(tr.downloadCount || 0);

    const logs = tr.logs || [];
    if (!logs.length) {
      $("transfer-logs").innerHTML = `<div class="muted-sm">${esc(t("transferNoLogs"))}</div>`;
    } else {
      $("transfer-logs").innerHTML = logs.map((l) => `
        <div class="log-row">
          <span class="mono">${esc(l.remoteAddr)}</span>
          <span>${esc(formatBytes(l.bytes))}</span>
          <span class="muted-sm">${esc(new Date(l.timestamp).toLocaleTimeString())}</span>
        </div>
      `).join("");
    }
  }

  // --- Accounts & Login Modal ---
  function renderAccounts() {
    const container = $("accounts-list");
    if (!state.accounts.length) {
      container.innerHTML = `<div class="empty-state"><p>${esc(t("accountsEmpty"))}</p></div>`;
      return;
    }
    container.innerHTML = state.accounts.map((a) => {
      const isActive = a.profileId === state.activeProfileId;
      const statusText = a.status === "valid" ? t("statusValid") : a.status === "expired" ? t("statusExpired") : t("statusUnknown");
      const dotCls = a.status === "valid" ? "valid" : a.status === "expired" ? "expired" : "";
      return `
        <div class="account-card ${isActive ? "active-card" : ""}">
          <div class="row-gap" style="justify-content:space-between">
            <strong>${esc(a.name || a.email)}</strong>
            ${isActive ? `<span class="badge">${esc(t("activeBadge"))}</span>` : ""}
          </div>
          <div class="muted-sm mt-8">${esc(a.email)}</div>
          <div class="app-meta-tags mt-8">
            <span class="tag"><span class="status-dot ${dotCls}" style="display:inline-block;margin-right:5px"></span>${esc(statusText)}</span>
            ${a.region ? `<span class="tag">${esc(a.region.toUpperCase())}</span>` : ""}
          </div>
          <div class="row-gap mt-16" style="flex-wrap:wrap">
            ${!isActive ? `<button class="btn btn-primary btn-sm" data-aaction="activate" data-id="${a.profileId}">${esc(t("btnUseAccount"))}</button>` : ""}
            <button class="btn btn-secondary btn-sm" data-aaction="verify" data-id="${a.profileId}">${esc(t("btnVerifyAccount"))}</button>
            <button class="btn btn-danger btn-sm" data-aaction="remove" data-id="${a.profileId}">${esc(t("btnRemoveAccount"))}</button>
          </div>
        </div>
      `;
    }).join("");

    container.querySelectorAll("button[data-aaction]").forEach((btn) => {
      const id = btn.dataset.id;
      btn.onclick = async () => {
        btn.disabled = true;
        try {
          if (btn.dataset.aaction === "activate") {
            await api(`/api/accounts/${id}/activate`, { method: "POST" });
            await refreshAccountsOnly();
          } else if (btn.dataset.aaction === "verify") {
            const res = await api(`/api/accounts/${id}/verify`, { method: "POST" });
            showToast(res.valid ? t("toastVerifiedValid") : (res.error || t("toastVerifiedExpired")), res.valid ? "success" : "warning");
            await refreshAccountsOnly();
          } else if (btn.dataset.aaction === "remove") {
            await api(`/api/accounts/${id}`, { method: "DELETE" });
            await refreshAccountsOnly();
          }
        } catch (err) {
          showToast(err.message, "error");
        } finally {
          btn.disabled = false;
        }
      };
    });
  }

  async function refreshAccountsOnly() {
    const snap = await api("/api/accounts");
    state.accounts = snap.accounts || [];
    state.activeProfileId = snap.activeProfileId || "";
    renderAccountPill();
    renderAccounts();
  }

  function openLoginModal() {
    state.challengeId = "";
    $("login-email").value = "";
    $("login-password").value = "";
    $("login-2fa-code").value = "";
    $("login-error-box").classList.add("hidden");
    $("twofa-error-box").classList.add("hidden");
    $("login-step1-form").classList.remove("hidden");
    $("login-step2-form").classList.add("hidden");
    $("login-modal-title").textContent = t("loginModalTitle");
    $("login-modal-backdrop").classList.remove("hidden");
    $("login-email").focus();
  }

  function closeLoginModal() {
    if (state.challengeId) {
      api("/api/accounts/login/cancel", { method: "POST", body: JSON.stringify({ challengeId: state.challengeId }) }).catch(() => {});
      state.challengeId = "";
    }
    $("login-modal-backdrop").classList.add("hidden");
  }

  // --- Diagnostics ---
  function renderDiagnostics() {
    const sys = state.system;
    if (!sys) return;
    const rows = [
      {
        label: t("diagIpatool"),
        val: sys.ipatoolFound ? `${t("diagFound")} (${sys.ipatoolVersion || "?"}) — ${sys.ipatoolPath}` : t("diagMissing")
      },
      {
        label: t("diagUnicorn"),
        val: sys.unicornFound ? `${t("diagFound")} — ${sys.unicornPath}` : t("diagMissing")
      },
      {
        label: t("diagCoreFp"),
        val: sys.coreFpReady ? `${t("diagCoreFpReady")} (${sys.coreFpCacheDir})` : `${t("diagCoreFpPending")}`
      },
      {
        label: t("diagDataDir"),
        val: sys.dataDir
      },
      {
        label: t("diagAppVer"),
        val: `v${sys.appVersion}`
      }
    ];
    $("diag-list").innerHTML = rows.map((r) => `
      <div class="diag-item">
        <div class="diag-label">${esc(r.label)}</div>
        <div class="diag-val">${esc(r.val)}</div>
      </div>
    `).join("");
  }

  // --- SSE ---
  function connectSSE() {
    const es = new EventSource("/api/events");
    es.addEventListener("download_update", (ev) => {
      try {
        upsertDownloadTask(JSON.parse(ev.data));
      } catch {}
    });
    es.addEventListener("download_removed", (ev) => {
      try {
        const d = JSON.parse(ev.data);
        state.downloads = state.downloads.filter((x) => x.id !== d.id);
        updateDownloadsBadge();
        renderDownloads();
      } catch {}
    });
    es.addEventListener("version_metadata", (ev) => {
      try {
        const payload = JSON.parse(ev.data);
        if (payload.appId === state.drawer.appId && payload.version) {
          const v = payload.version;
          state.drawer.hydrating.delete(v.externalVersionId);
          const idx = state.drawer.versions.findIndex((x) => x.externalVersionId === v.externalVersionId);
          if (idx !== -1) {
            state.drawer.versions[idx] = { ...state.drawer.versions[idx], ...v };
            renderDrawerTable();
          }
        }
      } catch {}
    });
    es.addEventListener("transfer_event", (ev) => {
      try {
        state.transfer = JSON.parse(ev.data);
        renderTransfer();
      } catch {}
    });
    es.addEventListener("account_update", (ev) => {
      try {
        const snap = JSON.parse(ev.data);
        state.accounts = snap.accounts || [];
        state.activeProfileId = snap.activeProfileId || "";
        renderAccountPill();
        renderAccounts();
      } catch {}
    });
  }

  function bindEvents() {
    document.querySelectorAll(".nav-item").forEach((btn) => {
      btn.onclick = () => switchView(btn.dataset.view);
    });
    $("sidebar-account-pill").onclick = () => switchView("accounts");

    $("search-form").onsubmit = handleSearchSubmit;
    $("direct-versions-btn").onclick = handleDirectVersionsClick;

    $("purchases-refresh-btn").onclick = () => loadPurchases(1);
    $("purchases-prev").onclick = () => loadPurchases(Math.max(1, state.purchases.page - 1));
    $("purchases-next").onclick = () => loadPurchases(state.purchases.page + 1);

    document.querySelectorAll("#download-filters .pill").forEach((p) => {
      p.onclick = () => {
        document.querySelectorAll("#download-filters .pill").forEach((x) => x.classList.remove("active"));
        p.classList.add("active");
        state.downloadFilter = p.dataset.filter;
        renderDownloads();
      };
    });
    $("open-download-dir-btn").onclick = async () => {
      if (!state.settings || !state.settings.downloadDir) return;
      try {
        await api("/api/system/reveal", { method: "POST", body: JSON.stringify({ path: state.settings.downloadDir }) });
      } catch (err) {
        showToast(err.message, "error");
      }
    };

    // Transfer
    $("transfer-completed-select").onchange = () => {
      if ($("transfer-completed-select").value) {
        $("transfer-file-path").value = $("transfer-completed-select").value;
      }
    };
    $("transfer-pick-file-btn").onclick = async () => {
      try {
        const res = await api("/api/system/pick-ipa", { method: "POST", body: "{}" });
        if (res.path) $("transfer-file-path").value = res.path;
      } catch (err) {
        showToast(err.message, "error");
      }
    };
    $("transfer-refresh-adapters").onclick = loadTransferInterfaces;
    $("transfer-start-btn").onclick = async () => {
      const filePath = $("transfer-file-path").value.trim();
      const bindIp = $("transfer-adapter-select").value;
      if (!filePath) {
        showToast(t("transferNoFile"), "warning");
        return;
      }
      try {
        state.transfer = await api("/api/transfer/start", {
          method: "POST",
          body: JSON.stringify({ filePath, bindIp })
        });
        renderTransfer();
      } catch (err) {
        showToast(err.message, "error");
      }
    };
    $("transfer-stop-btn").onclick = async () => {
      try {
        state.transfer = await api("/api/transfer/stop", { method: "POST" });
        renderTransfer();
      } catch (err) {
        showToast(err.message, "error");
      }
    };
    document.querySelectorAll("[data-qr-mode]").forEach((btn) => {
      btn.onclick = () => {
        document.querySelectorAll("[data-qr-mode]").forEach((x) => x.classList.remove("active"));
        btn.classList.add("active");
        state.qrMode = btn.dataset.qrMode;
        renderTransfer();
      };
    });
    $("transfer-copy-url-btn").onclick = async () => {
      const val = $("transfer-active-url").value;
      if (!val) return;
      try {
        await navigator.clipboard.writeText(val);
        showToast(t("copiedToast"), "success");
      } catch {
        $("transfer-active-url").select();
        document.execCommand("copy");
        showToast(t("copiedToast"), "success");
      }
    };

    // Drawer
    $("drawer-close-btn").onclick = () => $("version-drawer-backdrop").classList.add("hidden");
    $("version-drawer-backdrop").onclick = (e) => {
      if (e.target === $("version-drawer-backdrop")) $("version-drawer-backdrop").classList.add("hidden");
    };
    $("drawer-filter-input").oninput = () => {
      state.drawer.filter = $("drawer-filter-input").value;
      state.drawer.page = 1;
      renderDrawerTable();
      hydrateVisiblePage();
    };
    $("drawer-prev-page").onclick = () => {
      if (state.drawer.page > 1) {
        state.drawer.page--;
        renderDrawerTable();
        hydrateVisiblePage();
      }
    };
    $("drawer-next-page").onclick = () => {
      state.drawer.page++;
      renderDrawerTable();
      hydrateVisiblePage();
    };
    $("drawer-purchase-btn").onclick = acquireDrawerLicense;
    $("drawer-manual-btn").onclick = () => {
      const extId = $("drawer-manual-id").value.trim();
      if (!extId) return;
      enqueueDownload({
        appId: state.drawer.appId,
        bundleId: state.drawer.bundleId,
        appName: state.drawer.appName,
        artworkUrl: state.drawer.artworkUrl,
        platform: state.drawer.platform,
        storefront: state.drawer.storefront,
        externalVersionId: extId,
        displayVersion: ""
      });
    };

    // Login modal
    $("open-login-modal-btn").onclick = openLoginModal;
    $("login-modal-close").onclick = closeLoginModal;
    $("login-cancel-btn").onclick = closeLoginModal;
    $("twofa-cancel-btn").onclick = closeLoginModal;

    $("login-step1-form").onsubmit = async (e) => {
      e.preventDefault();
      const errBox = $("login-error-box");
      errBox.classList.add("hidden");
      const btn = $("login-submit-btn");
      btn.disabled = true;
      try {
        const res = await api("/api/accounts/login", {
          method: "POST",
          body: JSON.stringify({
            email: $("login-email").value.trim(),
            password: $("login-password").value,
            region: $("login-region").value
          })
        });
        if (res.needs2fa) {
          state.challengeId = res.challengeId;
          $("login-step1-form").classList.add("hidden");
          $("login-step2-form").classList.remove("hidden");
          $("login-modal-title").textContent = t("twoFaTitle");
          $("login-2fa-code").focus();
        } else {
          $("login-modal-backdrop").classList.add("hidden");
          await refreshAccountsOnly();
        }
      } catch (err) {
        errBox.textContent = err.message;
        errBox.classList.remove("hidden");
      } finally {
        btn.disabled = false;
      }
    };

    $("login-step2-form").onsubmit = async (e) => {
      e.preventDefault();
      const errBox = $("twofa-error-box");
      errBox.classList.add("hidden");
      const btn = $("twofa-submit-btn");
      btn.disabled = true;
      try {
        await api("/api/accounts/login/2fa", {
          method: "POST",
          body: JSON.stringify({
            challengeId: state.challengeId,
            authCode: $("login-2fa-code").value.trim()
          })
        });
        state.challengeId = "";
        $("login-modal-backdrop").classList.add("hidden");
        await refreshAccountsOnly();
      } catch (err) {
        errBox.textContent = err.message;
        errBox.classList.remove("hidden");
      } finally {
        btn.disabled = false;
      }
    };

    // Settings
    $("set-language").onchange = () => {
      state.lang = $("set-language").value;
      applyI18n();
    };
    $("set-theme").onchange = () => {
      applyTheme($("set-theme").value);
    };
    $("set-browse-dir-btn").onclick = async () => {
      try {
        const res = await api("/api/system/pick-folder", {
          method: "POST",
          body: JSON.stringify({ initialDir: $("set-download-dir").value })
        });
        if (res.path) $("set-download-dir").value = res.path;
      } catch (err) {
        showToast(err.message, "error");
      }
    };
    $("settings-form").onsubmit = async (e) => {
      e.preventDefault();
      try {
        const saved = await api("/api/settings", {
          method: "PUT",
          body: JSON.stringify({
            language: $("set-language").value,
            theme: $("set-theme").value,
            defaultStorefront: $("set-storefront").value,
            defaultPlatform: $("set-platform").value,
            downloadDir: $("set-download-dir").value.trim(),
            maxConcurrentDownloads: Number($("set-concurrency").value),
            autoPurchaseFree: $("set-auto-purchase").checked,
            customIpatoolPath: $("set-custom-ipatool").value.trim()
          })
        });
        state.settings = saved;
        state.system = await api("/api/system/status");
        renderDiagnostics();
        showToast(t("settingsSavedToast"), "success");
      } catch (err) {
        showToast(err.message, "error");
      }
    };
  }

  document.addEventListener("DOMContentLoaded", async () => {
    bindEvents();
    connectSSE();
    try {
      await refreshBootstrap();
    } catch (err) {
      showToast(err.message, "error");
    }
  });
})();
