// zdev's status line for pi, the same as the one for Claude Code:
// folder + sandbox marker + model + git branch + context + (subscription window | cost)
//
// The subscription windows come from Anthropic's response headers (the same
// ones Claude Code shows); with API-key billing there are none, and the
// session's cost shows instead. "extra" marks requests billed as extra usage.
function zdevStatusLine(pi) {
  const ORANGE = "\x1b[38;2;240;118;35m";
  const BLUE = "\x1b[38;2;75;149;233m";
  const GREEN = "\x1b[38;2;122;186;62m";
  const YELLOW = "\x1b[38;2;243;174;53m";
  const MAGENTA = "\x1b[38;2;198;120;221m";
  const RESET = "\x1b[0m";
  const BOLD = "\x1b[1m";
  const H = "anthropic-ratelimit-unified-";
  const ansi = /\x1b\[[0-9;]*m/g;

  // Utilization comes as a fraction (0.42) or a percentage (42).
  const percent = (v) => {
    if (v === undefined || v === "") return undefined;
    const n = Number.parseFloat(v);
    if (Number.isNaN(n)) return undefined;
    return Math.round(n <= 1 ? n * 100 : n);
  };

  let limits = {};
  let rerender;

  pi.on("after_provider_response", (event) => {
    const h = (event && event.headers) || {};
    const h5 = percent(h[`${H}5h-utilization`]);
    const d7 = percent(h[`${H}7d-utilization`]);
    if (h5 === undefined && d7 === undefined) return; // not a subscription response
    const reset = Number(h[`${H}5h-reset`]);
    limits = {
      h5,
      d7,
      reset: Number.isFinite(reset) && reset > 0 ? reset : undefined,
      overage: h[`${H}overage-in-use`] === "true",
    };
    if (rerender) rerender();
  });

  pi.on("session_start", (_event, ctx) => {
    if (!ctx.hasUI) return;
    // After every other session_start handler: a footer extension of the
    // developer's own, loaded from ~/.pi/agent/extensions, must not win.
    setTimeout(() => ctx.ui.setFooter((tui, _theme, footerData) => {
      rerender = () => tui.requestRender();
      const unsub = footerData.onBranchChange(() => tui.requestRender());
      return {
        dispose() {
          if (unsub) unsub();
          rerender = undefined;
        },
        invalidate() {},
        render(width) {
          const parts = [];
          // In a zdev sandbox the source sits at the service's mount path: name the project's folder.
          const cwd = ctx.cwd || process.cwd();
          const folder = process.env.ZDEV_HOST_PROJECTDIR || cwd.split("/").filter(Boolean).pop() || cwd;
          parts.push(`${BOLD}${ORANGE}${folder}${RESET}`);
          if (process.env.ZDEV_SANDBOX) parts.push(`${MAGENTA}⬢ sandbox${RESET}`);

          const model = ctx.model && (ctx.model.name || ctx.model.id);
          if (model) parts.push(`${BLUE}${model}${RESET}`);

          const branch = footerData.getGitBranch && footerData.getGitBranch();
          if (branch) parts.push(`${GREEN}${branch}${RESET}`);

          const usage = ctx.getContextUsage && ctx.getContextUsage();
          if (usage && usage.percent != null) {
            const u = Math.round(usage.percent);
            parts.push(u >= 80 ? `${YELLOW}ctx:${u}%${RESET}` : `ctx:${u}%`);
          }

          if (limits.h5 !== undefined) {
            let label = `5h ${limits.h5}%`;
            if (limits.reset) {
              const d = new Date(limits.reset * 1000);
              label += ` ⏳${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
            }
            if (limits.d7 !== undefined) label += ` · 7d ${limits.d7}%`;
            const hot = limits.h5 >= 80 || (limits.d7 || 0) >= 80;
            parts.push(hot ? `${YELLOW}${label}${RESET}` : label);
            if (limits.overage) parts.push(`${YELLOW}extra${RESET}`);
          } else {
            let cost = 0;
            const branchEntries = (ctx.sessionManager && ctx.sessionManager.getBranch && ctx.sessionManager.getBranch()) || [];
            for (const e of branchEntries) {
              if (e.type === "message" && e.message && e.message.role === "assistant") {
                cost += (e.message.usage && e.message.usage.cost && e.message.usage.cost.total) || 0;
              }
            }
            if (cost > 0) parts.push(`$${(Math.round(cost * 100) / 100).toString()}`);
          }

          const line = parts.join("  ");
          const plain = line.replace(ansi, "");
          if (plain.length <= width) return [line];
          return [plain.slice(0, Math.max(0, width - 1)) + "…"];
        },
      };
    }), 0);
  });
}
