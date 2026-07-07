// Run-panel form enhancement (R6.5.4). The server renders typed inputs from the
// input_schema AND a raw-JSON textarea; both submit fine without JavaScript and
// the kernel remains the validation authority (R2.5). This module only toggles
// between the two editors for ergonomics — it performs no validation.

for (const panel of document.querySelectorAll("[data-run-panel]")) {
  const toggle = panel.querySelector("[data-raw-toggle]");
  const fields = panel.querySelector("[data-fields]");
  const raw = panel.querySelector("[data-raw]");
  const hasFields = panel.querySelector('[name="has_fields"]');
  if (!toggle || !raw) continue;

  const apply = (useRaw) => {
    toggle.setAttribute("aria-pressed", String(useRaw));
    toggle.textContent = useRaw ? "structured" : "edit as JSON";
    if (fields) fields.hidden = useRaw;
    raw.hidden = !useRaw;
    // The server chooses raw vs structured from has_fields; disabling it while
    // raw is active makes the raw editor authoritative on submit.
    if (hasFields) hasFields.disabled = useRaw;
    if (useRaw) raw.querySelector('textarea')?.dispatchEvent(new Event('input'));
  };
  toggle.addEventListener("click", () => apply(toggle.getAttribute("aria-pressed") !== "true"));
  apply(false);
}
