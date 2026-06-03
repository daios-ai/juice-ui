// Run-panel form enhancement (R6.5.4). The server renders typed inputs from the
// input_schema AND a raw-JSON textarea; both submit fine without JavaScript and
// the kernel remains the validation authority (R2.5). This module only toggles
// between the two editors for ergonomics — it performs no validation.

for (const panel of document.querySelectorAll("[data-run-panel]")) {
  const toggle = panel.querySelector("[data-raw-toggle]");
  const fields = panel.querySelector("[data-fields]");
  const raw = panel.querySelector("[data-raw]");
  if (!toggle || !raw) continue;

  const apply = () => {
    const useRaw = toggle.checked;
    if (fields) fields.hidden = useRaw;
    raw.hidden = !useRaw;
  };
  toggle.addEventListener("change", apply);
  apply();
}
