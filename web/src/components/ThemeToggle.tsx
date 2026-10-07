import { nextSchemeChoice, SCHEME_LABEL, type SchemeChoice } from "../lib/scheme";
import { Icon } from "./Icon";

export function ThemeToggle({ choice, onChange }: { choice: SchemeChoice; onChange: (next: SchemeChoice) => void }) {
  const next = nextSchemeChoice(choice);
  return (
    <button type="button" className="icon-button theme-toggle" aria-label="明暗切换" title={`当前：${SCHEME_LABEL[choice]}，点击切到${SCHEME_LABEL[next]}`} onClick={() => onChange(next)}>
      <Icon name={choice === "dark" ? "moon" : "sun"} />
    </button>
  );
}
