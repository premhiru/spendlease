document.addEventListener("click", async (event) => {
  const copyButton = event.target.closest("[data-copy-secret]");
  if (copyButton) {
    const input = copyButton.parentElement.querySelector("[data-secret]");
    if (!input) return;
    try {
      await navigator.clipboard.writeText(input.value);
      copyButton.textContent = "Copied";
    } catch (_) {
      input.focus();
      input.select();
      copyButton.textContent = "Press Ctrl+C";
    }
    return;
  }

  const snippetButton = event.target.closest("[data-copy-target]");
  if (snippetButton) {
    const target = document.getElementById(snippetButton.dataset.copyTarget);
    if (!target) return;
    const value = target.value || target.textContent;
    try {
      await navigator.clipboard.writeText(value);
      snippetButton.textContent = "Copied";
    } catch (_) {
      const range = document.createRange();
      range.selectNodeContents(target);
      const selection = window.getSelection();
      selection.removeAllRanges();
      selection.addRange(range);
      snippetButton.textContent = "Press Ctrl+C";
    }
    return;
  }

  const closeButton = event.target.closest("[data-close-access]");
  if (closeButton) {
    const workspace = closeButton.closest("#agent-access");
    if (workspace) workspace.replaceChildren();
  }
});

// htmx normally ignores non-2xx response bodies. Management handlers return
// safe HTML fragments for validation and conflict errors, so show those
// messages without turning authentication failures into page content.
document.addEventListener("htmx:beforeSwap", (event) => {
  const response = event.detail.xhr;
  const target = event.detail.target;
  if (!target || !["onboarding-result", "agent-access", "provider-settings"].includes(target.id)) return;
  if (![404, 409, 422, 500].includes(response.status)) return;
  if (!(response.getResponseHeader("Content-Type") || "").startsWith("text/html")) return;
  event.detail.shouldSwap = true;
  event.detail.isError = false;
});
