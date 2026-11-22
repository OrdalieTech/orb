<script>
  import { onDestroy, onMount, tick } from "svelte";
  import { SvelteMap } from "svelte/reactivity";
  import TranscriptRow from "./TranscriptRow.svelte";

  let {
    tabId,
    rows = [],
    earlier = false,
    loaded = false,
    asleep = false,
    streaming = false,
    status = "",
    onEarlier = () => {},
  } = $props();

  const OVERSCAN_ABOVE = 6;
  const OVERSCAN_BELOW = 8;

  let scroller = $state(null);
  let viewportHeight = $state(700);
  let scrollTop = $state(0);
  let pinned = $state(true);

  // Height cache keyed by row.k, persisted while this component stays mounted for the tab.
  const heightCache = new Map();
  let measureVersion = $state(0);

  // Persistent expanded state for actions & run groups inside this tab.
  const expandedMap = new SvelteMap();

  function estimateRowHeight(row) {
    if (!row) return 36;
    if (row.kind === "you") {
      const len = row.text?.length || 0;
      const lines = Math.max(1, Math.ceil(len / 68));
      return 48 + lines * 22 + (row.images?.length ? 116 : 0);
    }
    if (row.kind === "run") {
      return 30 + (row.images?.length ? 116 : 0);
    }
    if (row.kind === "note") return 32;
    if (row.kind === "md") {
      const b = row.block;
      if (!b) return 28;
      if (b.type === "code" || b.type === "art") {
        const lines = (b.text || "").split("\n").length;
        return 52 + Math.min(lines, 24) * 20;
      }
      if (b.type === "table") return 40 + (b.rows?.length || 2) * 32;
      if (b.type === "h") return 38;
      if (b.type === "rule") return 18;
      const textLen = (b.spans || []).reduce((n, s) => n + (s.t?.length || 0), 0);
      return Math.max(28, Math.ceil(textLen / 76) * 22 + 8);
    }
    return 36;
  }

  // Recompute cumulative prefix sums in Float64Array (O(n), <0.1ms for 5000 rows).
  let layout = $derived.by(() => {
    void measureVersion;
    const n = rows.length;
    const offsets = new Float64Array(n + 1);
    let y = 0;
    for (let i = 0; i < n; i++) {
      offsets[i] = y;
      const k = rows[i].k;
      const h = heightCache.get(k) ?? estimateRowHeight(rows[i]);
      y += h;
    }
    offsets[n] = y;
    return { offsets, total: y };
  });

  function lowerBound(offsets, n, target) {
    let lo = 0;
    let hi = n;
    while (lo < hi) {
      const mid = (lo + hi) >>> 1;
      if (offsets[mid + 1] <= target) lo = mid + 1;
      else hi = mid;
    }
    return lo;
  }

  let visibleRange = $derived.by(() => {
    const n = rows.length;
    if (n === 0) return { start: 0, end: 0, padTop: 0, padBottom: 0 };
    const { offsets, total } = layout;
    const first = lowerBound(offsets, n, scrollTop);
    const last = lowerBound(offsets, n, scrollTop + viewportHeight);
    const start = Math.max(0, first - OVERSCAN_ABOVE);
    const end = Math.min(n, last + 1 + OVERSCAN_BELOW);
    return {
      start,
      end,
      padTop: offsets[start],
      padBottom: Math.max(0, total - offsets[end]),
    };
  });

  let visibleRows = $derived(rows.slice(visibleRange.start, visibleRange.end));

  // Shared ResizeObserver for all currently mounted rows + viewport container.
  let rowObserver = null;
  let vpObserver = null;
  const observedEls = new Map(); // el -> row.k
  let rafMeasure = 0;
  let pendingHeightDelta = false;

  // Track tab switching and prepended earlier-messages anchoring.
  let prevTabId = "";
  let prevFirstKey = "";
  let prevLen = 0;
  let lastScrollTop = 0;

  onMount(() => {
    rowObserver = new ResizeObserver(entries => {
      let changed = false;
      for (const entry of entries) {
        const key = observedEls.get(entry.target);
        if (!key) continue;
        const h = entry.borderBoxSize?.[0]?.blockSize ?? entry.contentRect.height;
        if (h > 0) {
          const prev = heightCache.get(key);
          if (prev === undefined || Math.abs(prev - h) > 0.5) {
            heightCache.set(key, h);
            changed = true;
          }
        }
      }
      if (changed && !rafMeasure) {
        pendingHeightDelta = true;
        rafMeasure = requestAnimationFrame(() => {
          rafMeasure = 0;
          pendingHeightDelta = false;
          measureVersion++;
          if (pinned && scroller) {
            scroller.scrollTop = scroller.scrollHeight;
            scrollTop = scroller.scrollTop;
          }
        });
      }
    });

    vpObserver = new ResizeObserver(entries => {
      const h = entries[0]?.contentRect.height;
      if (h > 0) {
        viewportHeight = h;
        if (pinned && scroller) {
          scroller.scrollTop = scroller.scrollHeight;
          scrollTop = scroller.scrollTop;
        }
      }
    });

    if (scroller) {
      vpObserver.observe(scroller);
      viewportHeight = scroller.clientHeight || 700;
    }
  });

  onDestroy(() => {
    rowObserver?.disconnect();
    vpObserver?.disconnect();
    if (rafMeasure) cancelAnimationFrame(rafMeasure);
  });

  // Svelte action to register/unregister a row wrapper element with the shared ResizeObserver.
  function observeRow(el, key) {
    observedEls.set(el, key);
    rowObserver?.observe(el);
    return {
      update(nextKey) {
        observedEls.set(el, nextKey);
      },
      destroy() {
        rowObserver?.unobserve(el);
        observedEls.delete(el);
      },
    };
  }

  // Reset pinning on tab switch; preserve scroll position on prepend (`earlier`); follow tail when pinned.
  $effect(() => {
    const currentTab = tabId;
    const len = rows.length;
    const firstKey = rows[0]?.k || "";

    if (currentTab !== prevTabId) {
      prevTabId = currentTab;
      prevFirstKey = firstKey;
      prevLen = len;
      pinned = true;
      heightCache.clear();
      measureVersion++;
      tick().then(() => {
        if (scroller) {
          scroller.scrollTop = scroller.scrollHeight;
          scrollTop = scroller.scrollTop;
        }
      });
      return;
    }

    // Detect prepended rows (`earlier messages`)
    if (prevFirstKey && firstKey && firstKey !== prevFirstKey && len > prevLen && !pinned) {
      const shiftIndex = rows.findIndex(r => r.k === prevFirstKey);
      if (shiftIndex > 0) {
        let addedHeight = 0;
        for (let i = 0; i < shiftIndex; i++) {
          addedHeight += heightCache.get(rows[i].k) ?? estimateRowHeight(rows[i]);
        }
        prevFirstKey = firstKey;
        prevLen = len;
        tick().then(() => {
          if (scroller) {
            scroller.scrollTop += addedHeight;
            scrollTop = scroller.scrollTop;
          }
        });
        return;
      }
    }

    prevFirstKey = firstKey;
    prevLen = len;

    if (pinned) {
      tick().then(() => {
        if (scroller && pinned) {
          scroller.scrollTop = scroller.scrollHeight;
          scrollTop = scroller.scrollTop;
        }
      });
    }
  });

  function onScroll() {
    if (!scroller) return;
    const top = scroller.scrollTop;
    scrollTop = top;
    const distFromBottom = scroller.scrollHeight - top - scroller.clientHeight;
    if (top > lastScrollTop && distFromBottom < 10) {
      pinned = true;
    } else if (top < lastScrollTop && distFromBottom > 24) {
      pinned = false;
    }
    lastScrollTop = top;
  }

  function onWheel(e) {
    if (e.deltaY < 0) pinned = false;
  }

  function onTouchMove() {
    pinned = false;
  }

  function onKeyDown(e) {
    if (["ArrowUp", "PageUp", "Home"].includes(e.key)) pinned = false;
    if (e.key === "End") scrollToBottom();
  }

  export function scrollToBottom() {
    pinned = true;
    if (scroller) {
      scroller.scrollTop = scroller.scrollHeight;
      scrollTop = scroller.scrollTop;
    }
  }

  function handleToggle(key) {
    if (expandedMap.get(key)) expandedMap.delete(key);
    else expandedMap.set(key, true);
  }
</script>

<div class="transcript-shell">
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div
    class="transcript scroll"
    role="log"
    aria-live="off"
    aria-label="Messages"
    bind:this={scroller}
    onscroll={onScroll}
    onwheel={onWheel}
    ontouchmove={onTouchMove}
    onkeydown={onKeyDown}
    tabindex="-1"
  >
    <div class="chat-col">
      {#if earlier}
        <button class="earlier-btn btn subtle" type="button" onclick={() => { pinned = false; onEarlier(); }}>
          ↑ earlier messages
        </button>
      {/if}

      {#if rows.length === 0}
        <div class="empty-state">
          {#if !loaded}
            <i class="led round live"></i>
            <span>loading…</span>
          {:else if asleep}
            <span>asleep · a message wakes it</span>
          {:else}
            <span class="empty-mark"></span>
            <span>ready</span>
          {/if}
        </div>
      {:else}
        <div class="virt-list" style="padding-top:{visibleRange.padTop}px;padding-bottom:{visibleRange.padBottom}px;">
          {#each visibleRows as row (row.k)}
            <div class="virt-row" use:observeRow={row.k}>
              <TranscriptRow {row} {tabId} {expandedMap} onToggle={handleToggle} />
            </div>
          {/each}
        </div>
      {/if}

      <div class="tail-space">
        {#if streaming}
          <span class="caret" aria-hidden="true"></span>
        {/if}
        {#if status}
          <p class="status-line">{status}</p>
        {/if}
      </div>
    </div>
  </div>

  {#if !pinned && rows.length > 0}
    <button class="jump-live btn" type="button" onclick={scrollToBottom} title="Jump to bottom (End)">
      ↓ live
    </button>
  {/if}
</div>

<style>
  .transcript-shell {
    position: relative;
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
  .transcript {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    overflow-anchor: none;
    overflow-wrap: anywhere;
    padding: 16px var(--margin) 0;
  }
  .transcript:focus-visible { outline: 0; }
  .chat-col {
    max-width: 780px;
    margin-inline: auto;
  }
  .earlier-btn {
    display: flex;
    margin: 0 auto 14px;
    font-size: 11.5px;
    padding: 4px 12px;
    border-radius: 0;
  }
  .empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 12px;
    padding: 64px 0;
    font-size: var(--label);
    font-weight: 700;
    letter-spacing: 0.12em;
    text-transform: uppercase;
    color: var(--meta);
    animation: view-in var(--t-med) var(--ease);
  }
  .empty-mark {
    width: 24px;
    height: 24px;
    background: var(--rule);
    mask: url(ordalie.svg) center / contain no-repeat;
  }
  .virt-row {
    contain: layout style;
  }
  .tail-space {
    min-height: 40px;
    padding: 8px 0 12px;
  }
  .caret {
    display: block;
    width: 8px;
    height: 16px;
    border-radius: 0;
    background: var(--rupture);
    animation: led-step 0.9s steps(1) infinite;
  }
  .status-line {
    font-size: 12px;
    color: var(--meta);
    padding-top: 6px;
  }
  .jump-live {
    position: absolute;
    right: 20px;
    bottom: 12px;
    padding: 4px 10px;
    border-radius: 0;
    font-size: 11px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    background: var(--fg);
    color: var(--bg);
    animation: drop-in var(--t-fast) var(--ease);
    z-index: 2;
  }
</style>
