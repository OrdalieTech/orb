<script>
  import { orb } from "../lib/store.svelte.js";
  import {
    newerVersion,
    PLUGIN_ABOUT,
    resetsAt,
    SHORTCUT_GROUPS,
    shortPeer,
    tabName,
  } from "../lib/utils.js";

  const PAGES = [
    { id: "general", idx: "01", label: "General" },
    { id: "providers", idx: "02", label: "Providers" },
    { id: "plugins", idx: "03", label: "Plugins" },
    { id: "mitosis", idx: "04", label: "Mitosis" },
  ];

  let pageName = $derived(orb.settingsPage.name);
  let peerId = $derived(orb.settingsPage.peer || orb.defaultPeer());
  let vendorId = $derived(orb.settingsPage.vendor || "");

  let activeMachine = $derived(orb.getMachine(peerId));
  let deviceList = $derived([
    ...(orb.home.machines || []).filter(m => !m.self && m.connected),
    ...(orb.home.machines || []).filter(m => m.self),
  ]);

  // Providers state
  let providerQuery = $state("");
  let loadingProviders = $state(false);
  let loadingAccounts = $state(false);
  let selfModelCounts = $state(new Map());
  let secretText = $state("");
  let secretVisible = $state(false);
  let autoCopiedCode = "";

  // Plugins state
  let tabPlugins = $state({});
  let remotePlugins = $state([]);
  let loadingPlugins = $state(false);

  // Mitosis state
  let syncGroup = $state(null);
  let updatingPeer = $state({});

  // Load page data reactively whenever page/peer/vendor changes
  $effect(() => {
    const p = pageName;
    const peer = peerId;
    const vendor = vendorId;
    if (!orb.rawTab) return;

    if (p === "general") {
      const aim = orb.getAim();
      if (aim.machine?.id) orb.ensureDefaults(aim.machine.id);
    } else if (p === "providers") {
      loadProviders(peer, vendor);
    } else if (p === "plugins") {
      loadPlugins(peer);
    } else if (p === "mitosis") {
      loadMitosis();
    }
  });

  // Auto-copy device code on first appearance
  $effect(() => {
    const f = orb.state.login;
    if (f?.state === "code" && f.code && f.code !== autoCopiedCode) {
      autoCopiedCode = f.code;
      navigator.clipboard?.writeText(f.code).then(() => orb.showToast("code copied")).catch(() => {});
    }
  });

  async function loadProviders(peer, vendor) {
    const m = orb.getMachine(peer);
    if (!m || m.self) {
      loadingAccounts = true;
      try {
        const [rawAccounts, avail] = await Promise.all([
          orb.rawTab.accounts().catch(() => []),
          orb.rawTab.rpc("get_available_models").catch(() => ({ models: [] })),
        ]);
        const sorted = [...(rawAccounts || [])].sort(
          (a, b) =>
            (a.provider_name || a.provider).localeCompare(b.provider_name || b.provider) ||
            Number(b.active) - Number(a.active) ||
            a.name.localeCompare(b.name),
        );
        orb.setAccounts(orb.state.self, sorted);
        const counts = new Map();
        for (const model of avail?.models || []) {
          counts.set(model.provider, (counts.get(model.provider) || 0) + 1);
        }
        selfModelCounts = counts;
        orb.ensureDefaults(orb.state.self);
      } finally {
        loadingAccounts = false;
      }
      return;
    }
    if (vendor) return;
    loadingProviders = !orb.providersByPeer[peer];
    loadingAccounts = !orb.accountsByPeer[peer];
    orb.ensureDefaults(peer);
    orb.ask("providers", { machine: peer }).then(
      list => {
        orb.setProviders(peer, list || []);
        loadingProviders = false;
      },
      err => {
        loadingProviders = false;
        orb.showError(err.message);
      },
    );
    orb.ask("accounts", { machine: peer }).then(
      list => {
        orb.setAccounts(peer, list || []);
        loadingAccounts = false;
      },
      () => {
        loadingAccounts = false;
      },
    );
  }

  async function loadPlugins(peer) {
    const m = orb.getMachine(peer);
    loadingPlugins = true;
    try {
      if (!m || m.self) {
        tabPlugins = (await orb.rawTab.plugins()) || {};
      } else {
        remotePlugins = (await orb.ask("plugins", { machine: peer })) || [];
      }
    } catch (err) {
      orb.showError(err.message);
    } finally {
      loadingPlugins = false;
    }
  }

  async function loadMitosis() {
    syncGroup = await orb.rawTab.admin("sync").catch(() => null);
  }

  async function toggleTabPlugin(name, on) {
    try {
      tabPlugins = await orb.rawTab.plugins(name, on);
    } catch (err) {
      orb.showError(err.message);
    }
  }

  async function changeRemotePlugin(args) {
    try {
      await orb.ask("plugin", { machine: peerId, ...args });
      await loadPlugins(peerId);
    } catch (err) {
      orb.showError(err.message);
    }
  }

  async function pairAnother() {
    const text = (await orb.pick("pair a device", [], { field: true, hint: "paste code from orb mitosis --browser" }))?.trim();
    if (!text) return;
    try {
      await orb.joinInvitation(text, true);
      await loadMitosis();
    } catch (err) {
      orb.showError(err.message);
    }
  }

  async function syncNow() {
    try {
      syncGroup = await orb.rawTab.admin("sync");
      orb.showToast("synced");
    } catch (err) {
      orb.showError(err.message);
    }
  }

  async function updateRemoteDevice(m) {
    updatingPeer = { ...updatingPeer, [m.id]: `updating to ${orb.latestRelease || "latest"}…` };
    try {
      const res = await orb.ask("update", { machine: m.id });
      updatingPeer = { ...updatingPeer, [m.id]: typeof res === "string" && res ? res : "updated" };
    } catch (err) {
      updatingPeer = { ...updatingPeer, [m.id]: err.message || "update failed" };
    }
  }

  async function forgetDevice(m) {
    if ((await orb.pick(`forget ${m.name}?`, [{ label: "keep it" }, { label: "forget" }])) !== "forget") return;
    try {
      await orb.ask("forget", { machine: m.id });
      await loadMitosis();
    } catch (err) {
      orb.showError(err.message);
    }
  }

  function sendLoginAnswer() {
    const val = secretText.trim();
    if (!val) return;
    orb.send({ do: "login.answer", text: val });
    secretText = "";
    secretVisible = false;
  }

  let remoteProviders = $derived(orb.providersByPeer[peerId] || []);
  let remoteAccounts = $derived(orb.accountsByPeer[peerId] || []);
  let selfAccounts = $derived(orb.accountsByPeer[orb.state.self] || []);
  let filteredProviders = $derived.by(() => {
    const q = providerQuery.trim().toLowerCase();
    if (!q) return remoteProviders;
    return remoteProviders.filter(p => `${p.name} ${p.id}`.toLowerCase().includes(q));
  });
  let filteredAccounts = $derived.by(() => {
    const q = providerQuery.trim().toLowerCase();
    const list = activeMachine?.self || !activeMachine ? selfAccounts : remoteAccounts;
    if (!q) return list;
    return list.filter(a => `${a.name} ${a.provider_name || a.provider}`.toLowerCase().includes(q));
  });
  let peerDefault = $derived(orb.defaultsByPeer[peerId] || null);
  let activeVendor = $derived(
    remoteProviders.find(x => x.id === vendorId) || { id: vendorId, name: vendorId, methods: [] },
  );
  let loginFlow = $derived(
    orb.state.login?.machine === peerId && orb.state.login?.provider === vendorId ? orb.state.login : null,
  );
  let generalDefault = $derived.by(() => {
    const m = orb.getAim().machine;
    return m ? { machine: m, def: orb.defaultsByPeer[m.id] } : null;
  });
</script>

<section class="settings">
  <nav class="sidebar" aria-label="Settings sections">
    <div class="side-head">
      <h1>Settings</h1>
      <p class="meta">this tab and your devices</p>
    </div>

    {#each PAGES as p}
      <button
        type="button"
        class="side-item"
        class:on={pageName === p.id}
        onclick={() => orb.openSettings(p.id, peerId)}
      >
        <span class="side-idx">{p.idx}</span>
        <span>{p.label}</span>
      </button>
    {/each}

    <button
      type="button"
      class="side-item back-btn"
      onclick={() => { orb.screen = "app"; orb.bumpFocus(); }}
    >
      <span>‹ conversations</span>
      <kbd>Esc</kbd>
    </button>
  </nav>

  <div class="content scroll">
    <div class="col">
      <!-- 01 · GENERAL -->
      {#if pageName === "general"}
        <h2>General</h2>
        <p class="meta">defaults, appearance, machines, and keyboard shortcuts</p>

        <div class="sec">
          <div class="hud-label"><span class="idx">01</span><span>new conversations</span></div>
          <div class="row">
            <div class="row-main">
              <strong>default model</strong>
              <p class="meta">
                {#if generalDefault?.def?.default}
                  {generalDefault.def.default.split("/").slice(1).join("/")}
                  {generalDefault.def.thinking ? ` · ${generalDefault.def.thinking}` : ""}
                  {generalDefault.machine && !generalDefault.machine.self ? ` · ${generalDefault.machine.name}` : ""}
                {:else}
                  configure on model deck
                {/if}
              </p>
            </div>
            <button type="button" class="btn subtle" onclick={() => orb.openModelDeck()}>change ›</button>
          </div>
        </div>

        <div class="sec">
          <div class="hud-label"><span class="idx">02</span><span>appearance</span></div>
          <div class="row">
            <div class="row-main">
              <strong>theme</strong>
              <p class="meta">Light (warm aluminum) · Dark (matte black)</p>
            </div>
            <div class="seg">
              {#each ["auto", "light", "dark"] as mode}
                <button
                  type="button"
                  class:on={orb.theme === mode}
                  onclick={() => orb.applyTheme(mode)}
                >{mode}</button>
              {/each}
            </div>
          </div>
        </div>

        <div class="sec">
          <div class="hud-label"><span class="idx">03</span><span>machines</span></div>
          <div class="mini-magi">
            {#each orb.home.machines || [] as m (m.id)}
              {@const up = m.self ? orb.state.up : m.connected}
              <div class="mini-card" class:down={!up || m.outdated}>
                <span><i class="led" style="--hue: var(--hue{m.hue || 0})"></i> {m.self ? "this tab" : m.name}</span>
                <b>{up && !m.outdated ? "承認" : "否決"}</b>
              </div>
            {/each}
          </div>
        </div>

        <div class="sec">
          <div class="hud-label"><span class="idx">04</span><span>keyboard shortcuts</span></div>
          {#each SHORTCUT_GROUPS as g}
            <p class="sub-lbl">{g.title}</p>
            {#each g.items as [k, desc]}
              <div class="row compact">
                <kbd>{k}</kbd>
                <span class="meta">{desc}</span>
              </div>
            {/each}
          {/each}
        </div>

      <!-- 02 · PROVIDERS -->
      {:else if pageName === "providers"}
        {#if vendorId && activeMachine && !activeMachine.self}
          <button
            type="button"
            class="btn plain back-link"
            onclick={() => {
              if (loginFlow && !["done", "failed"].includes(loginFlow.state)) orb.send({ do: "login.cancel" });
              orb.openSettings("providers", peerId);
            }}
          >‹ providers</button>
          <h2>{activeVendor.name}</h2>
          <p class="meta">
            {[
              activeMachine.name,
              activeVendor.ready
                ? `ready through ${activeVendor.holds} · ${activeVendor.models} model${activeVendor.models === 1 ? "" : "s"}`
                : "not signed in",
            ].join(" · ")}
          </p>

          {#if loginFlow && loginFlow.state !== "failed"}
            <div class="flow-card">
              {#if loginFlow.state === "starting"}
                <p class="meta">{loginFlow.detail || "starting…"}</p>
              {:else if loginFlow.state === "browser"}
                <h3>Continue in the browser</h3>
                {#if loginFlow.url}
                  <a class="btn inverted" href={loginFlow.url} target="_blank" rel="noreferrer">open the sign-in page</a>
                {/if}
                <p class="meta">Finish on the page, then paste the code it shows or the final redirect URL here.</p>
                {#if loginFlow.prompt}
                  <div class="ans-row">
                    <input
                      type="text"
                      bind:value={secretText}
                      placeholder={loginFlow.prompt.placeholder || "paste code or redirect URL"}
                      onkeydown={e => { if (e.key === "Enter") sendLoginAnswer(); }}
                    />
                    <button type="button" class="btn inverted" onclick={sendLoginAnswer}>save</button>
                  </div>
                {/if}
              {:else if loginFlow.state === "code"}
                <span class="hud-label"><span class="idx">CODE</span><span>enter this code (copied)</span></span>
                <button
                  type="button"
                  class="code-big"
                  onclick={() => navigator.clipboard?.writeText(loginFlow.code).then(() => orb.showToast("code copied"))}
                >{loginFlow.code}</button>
                {#if loginFlow.url}
                  <a class="btn inverted" href={loginFlow.url} target="_blank" rel="noreferrer">open sign-in page</a>
                {/if}
                {#if loginFlow.detail}<p class="meta">{loginFlow.detail}</p>{/if}
              {:else if loginFlow.state === "asking"}
                <p class="meta">{loginFlow.prompt?.message || ""}</p>
                {#if loginFlow.prompt?.options?.length}
                  <div class="actions">
                    {#each loginFlow.prompt.options as opt, idx}
                      <button
                        type="button"
                        class="btn"
                        class:inverted={idx === 0}
                        onclick={() => orb.send({ do: "login.answer", text: opt.id })}
                      >{opt.label}</button>
                    {/each}
                  </div>
                {:else}
                  {@const isSec = loginFlow.prompt?.kind === "secret"}
                  <div class="ans-row">
                    <input
                      type={isSec && !secretVisible ? "password" : "text"}
                      bind:value={secretText}
                      placeholder={loginFlow.prompt?.placeholder || (isSec ? "paste it here" : "")}
                      autocomplete="off"
                      onkeydown={e => { if (e.key === "Enter") sendLoginAnswer(); }}
                    />
                    {#if isSec}
                      <button type="button" class="btn subtle" onclick={() => (secretVisible = !secretVisible)}>
                        {secretVisible ? "hide" : "show"}
                      </button>
                    {/if}
                    <button type="button" class="btn inverted" onclick={sendLoginAnswer}>save</button>
                  </div>
                {/if}
              {:else if loginFlow.state === "done"}
                <h3>Signed in</h3>
                <p class="meta">{activeMachine.name} holds the new credential; its conversations can use the models now.</p>
                <button
                  type="button"
                  class="btn inverted"
                  onclick={() => { orb.send({ do: "login.cancel" }); orb.openSettings("providers", peerId); }}
                >done</button>
              {/if}

              {#if loginFlow.state !== "done"}
                <button type="button" class="btn subtle" onclick={() => orb.send({ do: "login.cancel" })}>cancel</button>
              {/if}
            </div>
          {:else}
            {#if loginFlow?.state === "failed"}
              <p class="meta alarm">{loginFlow.detail || "sign-in failed"}</p>
            {/if}
            {#each activeVendor.methods || [] as meth}
              <div class="method-card">
                <span class="hud-label"><span class="idx">·</span><span>{meth.account ? "account" : "api key"}</span></span>
                <strong>{meth.label}</strong>
                <p class="meta">{meth.about}</p>
                <button
                  type="button"
                  class="btn"
                  class:inverted={!activeVendor.ready}
                  onclick={() => orb.send({ do: "login", machine: peerId, provider: vendorId, auth: meth.auth })}
                >
                  {meth.account ? (activeVendor.ready ? "sign in again" : "sign in") : (activeVendor.ready ? "replace key" : "add key")}
                </button>
              </div>
            {/each}
            {#if activeVendor.ready}
              {#if activeVendor.status === "oauth" || activeVendor.source === "stored"}
                <div class="actions">
                  <button
                    type="button"
                    class="btn danger"
                    onclick={() =>
                      orb.ask("logout", { machine: peerId, provider: vendorId }).then(
                        () => loadProviders(peerId, ""),
                        err => orb.showError(err.message),
                      )}
                  >
                    {activeVendor.status === "oauth" ? "sign out" : "remove key"}
                  </button>
                </div>
              {:else}
                <p class="meta ext-note">Configured outside the app ({activeVendor.source}); change it there.</p>
              {/if}
            {/if}
          {/if}
        {:else}
          <h2>Providers</h2>
          <div class="chips">
            {#each deviceList as d (d.id)}
              <button
                type="button"
                class="chip"
                class:on={d.id === peerId}
                onclick={() => { providerQuery = ""; orb.openSettings("providers", d.id); }}
              >{d.self ? "this tab" : d.name}</button>
            {/each}
          </div>

          <input
            class="search-in"
            type="search"
            bind:value={providerQuery}
            placeholder="Anthropic, OpenAI, Groq…"
            autocomplete="off"
            spellcheck="false"
          />

          {#if peerDefault?.default}
            <div class="row">
              <div class="row-main">
                <strong>default model</strong>
                <p class="meta">{peerDefault.default.split("/").slice(1).join("/")}{peerDefault.thinking ? ` · ${peerDefault.thinking}` : ""}</p>
              </div>
              <button type="button" class="btn subtle" onclick={() => orb.openModelDeck()}>change ›</button>
            </div>
          {/if}

          {#if !activeMachine || activeMachine.self}
            <p class="meta sub-gap">accounts synced to this tab</p>
            <div class="sec">
              <div class="hud-label"><span class="idx">01</span><span>accounts</span></div>
              {#if filteredAccounts.length > 0}
                {#each filteredAccounts as a (a.provider + "/" + a.id)}
                  {@const cnt = selfModelCounts.get(a.provider)}
                  <div class="row">
                    <div class="row-main">
                      <strong>{a.name}</strong>
                      <p class="meta">
                        {[
                          a.provider_name?.split(" (")[0] || a.provider,
                          a.auth === "oauth" ? "account" : "api key",
                          cnt ? `${cnt} model${cnt === 1 ? "" : "s"}` : "",
                        ].filter(Boolean).join(" · ")}
                      </p>
                    </div>
                    <button
                      type="button"
                      class="btn"
                      class:subtle={!a.active}
                      class:inverted={a.active}
                      onclick={() => !a.active && orb.switchAccount(orb.state.self, a.provider, a.id)}
                    >{a.active ? "in use" : "use ›"}</button>
                  </div>
                {/each}
              {:else}
                <p class="meta sub-gap">{loadingAccounts ? "reading accounts…" : "no shared accounts yet"}</p>
              {/if}
            </div>
          {:else}
            <p class="meta sub-gap">
              {activeMachine.name} · {loadingProviders ? "loading…" : `${remoteProviders.filter(p => p.ready).length} ready · ${remoteProviders.length} to choose from`}
            </p>

            {#if filteredAccounts.length > 0 || loadingAccounts}
              <div class="sec">
                <div class="hud-label"><span class="idx">01</span><span>accounts · quota left</span></div>
                {#if filteredAccounts.length > 0}
                  {#each filteredAccounts as a (a.provider + "/" + a.id)}
                    <div class="acct-block">
                      <div class="row no-border">
                        <div class="row-main">
                          <strong>{a.name}</strong>
                          <p class="meta">{[a.provider_name?.split(" (")[0], a.plan].filter(Boolean).join(" · ")}</p>
                        </div>
                        <button
                          type="button"
                          class="btn"
                          class:subtle={!a.active}
                          class:inverted={a.active}
                          onclick={() => !a.active && orb.switchAccount(peerId, a.provider, a.id)}
                        >{a.active ? "in use" : "use ›"}</button>
                      </div>
                      {#each a.windows || [] as w}
                        <div class="quota" class:alarm={w.left < 15}>
                          <small>{w.name}</small>
                          <span class="q-track"><i style="width:{Math.max(0, Math.min(100, w.left))}%"></i></span>
                          <small class="q-pct">{Math.round(w.left)}%</small>
                          <small class="q-rst">{resetsAt(w.resets)}</small>
                        </div>
                      {/each}
                    </div>
                  {/each}
                {:else}
                  <p class="meta sub-gap">reading accounts and quotas…</p>
                {/if}
              </div>
            {/if}

            {#each [
              ["02", "ready", filteredProviders.filter(p => p.ready)],
              ["03", "your subscription", filteredProviders.filter(p => !p.ready && p.methods?.some(x => x.account))],
              ["04", "api key", filteredProviders.filter(p => !p.ready && !p.methods?.some(x => x.account))],
            ] as [idx, title, list]}
              {#if list.length > 0}
                <div class="sec">
                  <div class="hud-label"><span class="idx">{idx}</span><span>{title}</span></div>
                  {#each list as p (p.id)}
                    <div class="row">
                      <div class="row-main">
                        <strong>{p.name}</strong>
                        <p class="meta">{p.ready ? p.holds : (p.methods || []).map(x => (x.account ? "account" : "api key")).join(" · ")}</p>
                      </div>
                      <button
                        type="button"
                        class="btn subtle"
                        onclick={() => orb.openSettings("providers", peerId, p.id)}
                      >
                        {p.ready ? `${p.models} model${p.models === 1 ? "" : "s"} ›` : p.methods?.some(x => x.account) ? "sign in ›" : "add key ›"}
                      </button>
                    </div>
                  {/each}
                </div>
              {/if}
            {/each}
          {/if}
        {/if}

      <!-- 03 · PLUGINS -->
      {:else if pageName === "plugins"}
        <h2>Plugins</h2>
        <div class="chips">
          {#each deviceList as d (d.id)}
            <button
              type="button"
              class="chip"
              class:on={d.id === peerId}
              onclick={() => orb.openSettings("plugins", d.id)}
            >{d.self ? "this tab" : d.name}</button>
          {/each}
        </div>

        {#if loadingPlugins}
          <p class="meta sub-gap">loading plugins…</p>
        {:else if !activeMachine || activeMachine.self}
          <p class="meta sub-gap">what this tab's Orb runs, from its next conversation</p>
          {#each Object.keys(tabPlugins).sort() as name (name)}
            <div class="row">
              <div class="row-main">
                <strong>{name}</strong>
                <p class="meta">{PLUGIN_ABOUT[name] || ""}</p>
              </div>
              <button
                type="button"
                class="btn"
                class:inverted={tabPlugins[name]}
                class:subtle={!tabPlugins[name]}
                onclick={() => toggleTabPlugin(name, !tabPlugins[name])}
              >{tabPlugins[name] ? "on" : "off"}</button>
            </div>
          {/each}
        {:else}
          <p class="meta sub-gap">
            {activeMachine.name} · {remotePlugins.filter(x => x.on).length} of {remotePlugins.length} on
          </p>
          {#each remotePlugins as pl (pl.name)}
            <div class="plugin-block">
              <div class="row no-border">
                <div class="row-main">
                  <strong>{pl.name}</strong>
                  <p class="meta">{pl.about}</p>
                </div>
                <button
                  type="button"
                  class="btn"
                  class:inverted={pl.on}
                  class:subtle={!pl.on}
                  onclick={() => changeRemotePlugin({ name: pl.name, on: !pl.on })}
                >{pl.on ? "on" : "off"}</button>
              </div>
              {#if pl.on && (pl.choices || []).length > 0}
                {#each pl.choices as c}
                  <div class="chips choice-row">
                    <small class="meta">{c.key}</small>
                    {#each c.values as v}
                      <button
                        type="button"
                        class="chip"
                        class:on={v === c.value}
                        onclick={() => v !== c.value && changeRemotePlugin({ name: pl.name, key: c.key, value: v })}
                      >{v}</button>
                    {/each}
                  </div>
                {/each}
              {/if}
            </div>
          {/each}
          <p class="meta sub-gap">An Orb reads its plugins when it starts: those open here reopen with a change at their next message.</p>
        {/if}

      <!-- 04 · MITOSIS -->
      {:else if pageName === "mitosis"}
        <h2>Mitosis</h2>
        <p class="meta">peer-to-peer mesh, device updates, and encrypted sync group</p>

        <div class="sec">
          <div class="hud-label">
            <span class="idx">01</span>
            <span>devices</span>
            <span class="tail">{(orb.home.machines || []).filter(m => !m.self && m.connected).length} connected</span>
          </div>

          {#each (orb.home.machines || []).filter(m => !m.self) as m (m.id)}
            {@const canUpdate = m.connected && (m.outdated || (orb.latestRelease && m.version && newerVersion(orb.latestRelease, m.version)))}
            <div class="row">
              <i class="led" class:live={m.running?.some(r => r.busy)} style="--hue: {m.connected ? `var(--hue${m.hue || 1})` : 'var(--rule)'}"></i>
              <button
                type="button"
                class="row-main clickable"
                onclick={() => {
                  if (m.running?.length) orb.openEntry(`i:${m.running[0].instance}`);
                  else if (m.launch && m.connected) orb.explorerPeer = m.id;
                }}
              >
                <strong>{m.name}</strong>
                <p class="meta">
                  {[
                    m.connected ? "connected" : "offline",
                    m.version && `orb ${m.version}`,
                    m.launch ? "starts conversations" : m.outdated ? "update needed" : "follows only",
                  ].filter(Boolean).join(" · ")}
                </p>
                {#if updatingPeer[m.id]}
                  <p class="meta">{updatingPeer[m.id]}</p>
                {/if}
              </button>
              <div class="row-btns">
                {#if canUpdate && !updatingPeer[m.id]}
                  <button type="button" class="btn inverted" onclick={() => updateRemoteDevice(m)}>update</button>
                {/if}
                <button
                  type="button"
                  class="btn subtle"
                  onclick={() => orb.renamePrompt(m.name, name => orb.send({ do: "rename", machine: m.id, name }))}
                >rename</button>
                <button type="button" class="btn danger" onclick={() => forgetDevice(m)}>forget</button>
              </div>
            </div>
          {:else}
            <p class="meta sub-gap">no devices paired yet</p>
          {/each}

          <div class="actions">
            <button type="button" class="btn inverted" onclick={pairAnother}>+ pair a device</button>
          </div>
        </div>

        {#if syncGroup?.group_id}
          {@const members = syncGroup.members || []}
          {@const owners = new Set(syncGroup.owners || [])}
          {@const names = syncGroup.names || {}}
          <div class="sec">
            <div class="hud-label">
              <span class="idx">02</span>
              <span>sync group · {syncGroup.in_sync ? "in sync" : "syncing"}</span>
              <span class="tail">{members.length} device{members.length === 1 ? "" : "s"}</span>
            </div>
            {#each members as id (id)}
              {@const m = orb.getMachine(id)}
              {@const display = id === orb.me ? (names[id] || tabName()) : (m?.name || names[id] || shortPeer(id))}
              {@const tags = [id === orb.me && "this tab", owners.has(id) && "owner", m && !m.self && (m.connected ? "connected" : "offline")].filter(Boolean)}
              <div class="row">
                <div class="row-main">
                  <strong>{display}</strong>
                  <p class="meta">{tags.join(" · ") || "sync member"}</p>
                </div>
              </div>
            {/each}
            <div class="actions">
              <button type="button" class="btn subtle" onclick={syncNow}>sync now</button>
            </div>
          </div>
        {/if}

        <div class="sec">
          <div class="hud-label"><span class="idx">{syncGroup?.group_id ? "03" : "02"}</span><span>this tab</span></div>
          <div class="row">
            <div class="row-main">
              <strong>{shortPeer(orb.me)}</strong>
              <p class="meta">{orb.me}</p>
            </div>
            <button
              type="button"
              class="btn subtle"
              onclick={() => navigator.clipboard?.writeText(orb.me).then(() => orb.showToast("peer id copied"))}
            >copy</button>
          </div>
          <p class="meta sub-gap">
            Each device is approved by fingerprint on its own screen. Forgetting this tab leaves the sync group and wipes local storage here.
          </p>
          <div class="actions">
            <button type="button" class="btn danger" onclick={() => orb.forgetTab()}>forget this tab</button>
          </div>
        </div>
      {/if}

      {#if orb.error}
        <p class="meta alarm err-gap" role="alert">{orb.error}</p>
      {/if}
    </div>
  </div>
</section>

<style>
  .settings {
    flex: 1;
    min-height: 0;
    display: flex;
    width: 100%;
    animation: view-in var(--t-med) var(--ease);
  }
  .sidebar {
    display: flex;
    flex-direction: column;
    gap: 0;
    width: 220px;
    flex-shrink: 0;
    padding: 0;
    border-right: 1px solid var(--rule);
    background: var(--bg);
  }
  .side-head { padding: 14px 14px 12px; border-bottom: 1px solid var(--rule); }
  .side-head h1 { font-size: var(--title); margin-bottom: 2px; }
  .side-item {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 14px;
    border: 0;
    border-bottom: 1px solid var(--rule-soft);
    border-radius: 0;
    background: none;
    text-align: left;
    font-size: var(--small);
    font-weight: 600;
    color: var(--mute);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .side-idx {
    display: none;
  }
  .side-item.on {
    color: var(--bg);
    font-weight: 700;
    background: var(--fg);
  }
  .side-item.on .side-idx { color: var(--rupture); }
  .back-btn {
    margin-top: auto;
    border-top: 1px solid var(--rule);
    border-bottom: 0;
    justify-content: space-between;
  }
  @media (hover: hover) {
    .side-item:hover:not(.on) { background: var(--hover); color: var(--fg); }
  }

  .content {
    flex: 1;
    min-width: 0;
    overflow-y: auto;
    padding: 24px 36px 48px;
  }
  .col {
    max-width: 660px;
    animation: view-in var(--t-fast) var(--ease);
  }
  h2 { font-size: 21px; margin-bottom: 4px; letter-spacing: -0.03em; }
  h3 { font-size: 15px; }

  .sec { margin-top: 26px; }
  .sub-lbl {
    margin: 14px 0 4px;
    font-size: 12px;
    font-weight: 700;
    color: var(--mute);
  }
  .sub-gap { margin-top: 8px; }
  .err-gap { margin-top: 14px; }
  .ext-note { margin-top: 12px; }
  .back-link { margin-bottom: 10px; padding-left: 0; }

  .row {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 12px 0;
    border-bottom: 1px solid var(--rule-soft);
  }
  .row.no-border { border-bottom: 0; }
  .row.compact { padding: 7px 0; justify-content: space-between; }
  .row-main { flex: 1; min-width: 0; }
  .row-main strong { font-weight: 550; }
  .row-main.clickable {
    border: 0;
    background: none;
    padding: 0;
    text-align: left;
    cursor: pointer;
  }
  .row-btns { display: flex; gap: 6px; flex-shrink: 0; }

  /* Machined TE segmented switch */
  .seg {
    display: inline-flex;
    border: 1px solid var(--rule);
    border-radius: var(--radius-sm);
    overflow: hidden;
  }
  .seg button {
    padding: 4px 11px;
    border: 0;
    border-right: 1px solid var(--rule);
    background: var(--raised);
    font-size: 11.5px;
    font-weight: 700;
    color: var(--mute);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .seg button:last-child { border-right: 0; }
  .seg button.on {
    background: var(--fg);
    color: var(--bg);
  }

  .mini-magi {
    display: flex;
    flex-wrap: wrap;
    gap: 0;
    margin-top: 8px;
    border: 1px solid var(--rule);
    width: fit-content;
  }
  .mini-card {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 6px 11px;
    border: 0;
    border-right: 1px solid var(--rule);
    background: var(--raised);
    font-size: 12px;
  }
  .mini-card:last-child { border-right: 0; }
  .mini-card span { display: flex; align-items: center; gap: 7px; font-weight: 600; }
  .mini-card b { font-size: 10px; font-weight: 700; color: var(--amber); }
  .mini-card.down b { color: var(--rupture); }

  .chips {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
    margin: 14px 0 10px;
  }
  .chip {
    padding: 4px 10px;
    border: 1px solid var(--rule);
    border-radius: var(--radius-sm);
    background: var(--raised);
    font-size: 12px;
    font-weight: 600;
    color: var(--mute);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), border-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .chip:hover:not(.on) { border-color: var(--rule-focus); color: var(--fg); }
  .chip.on {
    color: var(--bg);
    background: var(--fg);
    border-color: var(--fg);
  }
  .choice-row { margin: -2px 0 10px; }

  .search-in {
    width: 100%;
    padding: 7px 10px;
    margin: 4px 0 10px;
    border: 1px solid var(--rule);
    border-radius: var(--radius-sm);
    background: var(--raised);
    font-size: var(--small);
    transition: border-color var(--t-fast) var(--ease);
  }
  .search-in:focus-visible { outline: 0; border-color: var(--rule-focus); }

  .acct-block, .plugin-block {
    padding-bottom: 8px;
    border-bottom: 1px solid var(--rule);
  }
  .quota {
    display: grid;
    grid-template-columns: 78px 1fr 42px 88px;
    align-items: center;
    gap: 10px;
    padding: 4px 0 4px 12px;
    font-size: 11.5px;
    color: var(--mute);
  }
  .q-track {
    height: 6px;
    background: var(--recessed);
    border: 1px solid var(--rule);
    overflow: hidden;
  }
  .q-track i {
    display: block;
    height: 100%;
    background: var(--fg);
    transition: width 180ms var(--ease);
  }
  .quota.alarm .q-track i { background: var(--rupture); }
  .quota.alarm .q-pct { color: var(--rupture); font-weight: 700; }

  .method-card, .flow-card {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 10px;
    margin-top: 14px;
    padding: 16px;
    border: 1px solid var(--rule);
    border-radius: var(--radius-md);
    background: var(--raised);
  }
  .code-big {
    padding: 4px 0;
    border: 0;
    background: none;
    font-family: var(--font-mono);
    font-size: 32px;
    font-weight: 700;
    letter-spacing: 0.06em;
    cursor: pointer;
  }
  .ans-row {
    display: flex;
    gap: 8px;
    width: 100%;
  }
  .ans-row input {
    flex: 1;
    min-width: 0;
    padding: 8px 10px;
    border: 1px solid var(--rule);
    border-radius: 0;
    background: var(--bg);
    font-size: 13px;
  }
  .ans-row input:focus-visible { outline: 0; border-color: var(--fg); background: var(--raised); }

  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin-top: 16px;
  }

  @media (max-width: 680px) {
    .settings { flex-direction: column; }
    .sidebar {
      flex-direction: row;
      width: auto;
      padding: 8px;
      overflow-x: auto;
      scrollbar-width: none;
      border-right: 0;
      border-bottom: 1px solid var(--rule);
    }
    .side-head, .side-idx, .back-btn kbd { display: none; }
    .side-item { flex-shrink: 0; }
    .back-btn { order: -1; margin-top: 0; }
    .content { padding: 16px var(--margin) 32px; }
  }
</style>
