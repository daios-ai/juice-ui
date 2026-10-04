// Run-panel form enhancement (R6.5.4). The server renders typed inputs from the
// input_schema AND a raw-JSON textarea; both submit fine without JavaScript and
// the kernel remains the validation authority (R2.5). This module only toggles
// between the two editors for ergonomics — it performs no validation.

for (const panel of document.querySelectorAll("[data-run-panel]")) {
  const segFields = panel.querySelector('[data-raw-seg="fields"]');
  const segJson = panel.querySelector('[data-raw-seg="json"]');
  const fields = panel.querySelector("[data-fields]");
  const raw = panel.querySelector("[data-raw]");
  const hasFields = panel.querySelector('[name="has_fields"]');
  if (!segFields || !segJson || !raw) continue;

  const apply = (useRaw) => {
    segFields.setAttribute("aria-pressed", String(!useRaw));
    segJson.setAttribute("aria-pressed", String(useRaw));
    if (fields) {
      fields.hidden = useRaw;
      // A hidden field still takes part in constraint validation; disabled, it
      // neither blocks the raw editor's submit nor is sent with it.
      for (const el of fields.querySelectorAll("input, select, textarea")) el.disabled = useRaw;
    }
    raw.hidden = !useRaw;
    // The server chooses raw vs structured from has_fields; disabling it while
    // raw is active makes the raw editor authoritative on submit.
    if (hasFields) hasFields.disabled = useRaw;
    if (useRaw) raw.querySelector("textarea")?.dispatchEvent(new Event("input"));
  };
  segFields.addEventListener("click", () => apply(false));
  segJson.addEventListener("click", () => apply(true));
  apply(false);
}
