<script>
  import { onDestroy } from "svelte";
  import { orb } from "../lib/store.svelte.js";
  import { shortPeer } from "../lib/utils.js";

  let codeText = $state("");
  let joining = $state(false);
  let nowSec = $state(Math.floor(Date.now() / 1000));

  const timer = setInterval(() => {
    nowSec = Math.floor(Date.now() / 1000);
  }, 1000);
  onDestroy(() => clearInterval(timer));

  let parsed = $derived(orb.parseInvitationCode(codeText));
  let leftSec = $derived(parsed?.expires ? Math.max(0, parsed.expires - nowSec) : 0);
  let validTime = $derived(!parsed?.expires || leftSec > 0);

  async function submitJoin() {
    if (!codeText.trim()) return;
    joining = true;
    try {
      await orb.joinInvitation(codeText, false);
    } catch (err) {
      orb.showError(err.message);
    } finally {
      joining = false;
    }
  }
</script>

<section class="pair-shell">
  {#if orb.screen === "invite"}
    <div class="pair-card">
      <div class="hud-label"><span class="idx">01</span><span>mitosis · browser peer</span></div>
      <h1>Pair this tab</h1>
      <p class="sub">On your computer, run</p>
      <code class="cmd">orb mitosis --browser</code>
      <p class="meta">then paste the code it shows. It asks you to approve this tab there.</p>

      <textarea
        class="code-in"
        rows="3"
        bind:value={codeText}
        placeholder="orb-mitosis:v1:…"
        spellcheck="false"
      ></textarea>

      {#if parsed}
        <div class="preview">
          <span class="hud-label"><span class="idx">ID</span><span>invitation from</span></span>
          <strong>{shortPeer(parsed.peer_id)}</strong>
          <p class="meta">{parsed.peer_id}</p>
          {#if parsed.expires}
            <p class="meta" class:alarm={!validTime}>
              {validTime ? `valid ${Math.floor(leftSec / 60)}:${String(leftSec % 60).padStart(2, "0")} · single use` : "expired"}
            </p>
          {/if}
        </div>
      {/if}

      <button
        type="button"
        class="btn inverted"
        disabled={joining || !codeText.trim() || (parsed && !validTime)}
        onclick={submitJoin}
      >
        {joining ? "connecting…" : "pair"}
      </button>
    </div>
  {:else if orb.screen === "approve"}
    <div class="pair-card">
      <div class="hud-label"><span class="idx">02</span><span>waiting for approval</span></div>
      <h1>Approve on the computer</h1>
      <p class="sub">On the computer, answer <b>y</b>. It shows this tab as</p>
      <strong class="fp">{orb.fingerprint}</strong>
      <p class="meta">{orb.me}</p>
      <p class="wait-row"><i class="led round live"></i><span>waiting for the yes</span></p>
    </div>
  {:else if orb.screen === "elsewhere"}
    <div class="pair-card">
      <div class="hud-label"><span class="idx">!</span><span>tab lock</span></div>
      <h1>Open in another tab</h1>
      <p class="sub">{orb.elsewhereText}</p>
      <div class="actions">
        <button type="button" class="btn inverted" onclick={() => orb.takeOverTab()}>use here</button>
      </div>
    </div>
  {/if}

  {#if orb.error}
    <p class="err alarm" role="alert">{orb.error}</p>
  {/if}
</section>

<style>
  .pair-shell {
    flex: 1;
    display: flex;
    flex-direction: column;
    justify-content: center;
    width: 100%;
    max-width: 480px;
    margin: 0 auto;
    padding: var(--margin);
  }
  .pair-card {
    padding: 20px;
    border: 1px solid var(--rule);
    border-radius: var(--pane);
    background: var(--raised);
    box-shadow: 0 8px 28px var(--shade);
  }
  h1 {
    margin: 8px 0 8px;
    font-size: var(--title);
  }
  .sub { color: var(--mute); font-size: var(--small); }
  .cmd {
    display: inline-block;
    margin: 10px 0;
    padding: 7px 12px;
    border: 1px solid var(--rule);
    border-radius: var(--soft);
    background: var(--recessed);
    font-weight: 700;
  }
  .code-in {
    display: block;
    width: 100%;
    margin: 14px 0 12px;
    padding: 9px 11px;
    resize: none;
    font-size: 12px;
    border: 1px solid var(--rule);
    border-radius: var(--soft);
    background: var(--bg);
  }
  .code-in:focus-visible { outline: 0; border-color: var(--fg); }
  .preview {
    margin-bottom: 12px;
    padding: 10px 12px;
    border: 1px solid var(--rule);
    border-radius: var(--soft);
    background: var(--recessed);
  }
  .preview strong {
    display: block;
    margin: 4px 0 2px;
    font-size: 22px;
    letter-spacing: .03em;
  }
  .fp {
    display: block;
    margin: 10px 0 4px;
    font-size: 28px;
    letter-spacing: .04em;
  }
  .wait-row {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-top: 16px;
    font-size: var(--small);
    color: var(--mute);
  }
  .actions { margin-top: 16px; }
  .err { margin-top: 12px; font-size: var(--small); }
</style>
