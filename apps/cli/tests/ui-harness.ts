import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
import { transformSync } from "esbuild";

// Run the real owned component/hook with a deterministic render scheduler.
// Terminal rendering is replaced; transport and submission code stay real.
export function mount(source: URL, exportName: string, props: unknown, modules: Record<string, unknown>) {
  const slots: any[] = [];
  let cursor = 0;
  let pending = false;
  let effects: Array<() => void> = [];
  let render: () => void;
  let value: any;
  const schedule = () => {
    if (pending) return;
    pending = true;
    queueMicrotask(() => { pending = false; render(); });
  };
  const memo = (fn: () => any, deps: unknown[]) => {
    const index = cursor++;
    const old = slots[index];
    if (!old || deps.some((dep, i) => !Object.is(dep, old.deps[i]))) slots[index] = { deps, value: fn() };
    return slots[index].value;
  };
  const react = {
    createElement: (type: unknown, props: unknown, ...children: unknown[]) => ({type, props, children}),
    useState(initial: any) {
      const index = cursor++;
      if (!(index in slots)) slots[index] = typeof initial === "function" ? initial() : initial;
      return [slots[index], (next: any) => {
        const updated = typeof next === "function" ? next(slots[index]) : next;
        if (!Object.is(updated, slots[index])) { slots[index] = updated; schedule(); }
      }];
    },
    useRef(initial: any) { const index = cursor++; return slots[index] ??= {current: initial}; },
    useMemo: memo,
    useCallback: (fn: any, deps: unknown[]) => memo(() => fn, deps),
    useEffect(fn: () => void, deps: unknown[]) { memo(() => { effects.push(fn); }, deps); },
  };
  const code = transformSync(readFileSync(source, "utf8"), {loader: source.pathname.endsWith("tsx") ? "tsx" : "ts", format:"cjs"}).code;
  const exports = {};
  const module = {exports};
  runInNewContext(code, {
    exports, module, console, queueMicrotask, setTimeout, clearTimeout,
    require: (name: string) => {
      if (name === "react") return react;
      if (name in modules) return modules[name];
      throw new Error(`unmocked UI dependency: ${name}`);
    },
  });
  render = () => {
    cursor = 0;
    effects = [];
    value = (module.exports as any)[exportName](props);
    for (const effect of effects) effect();
  };
  render();
  return {get current() {return value;}};
}

export const flush = () => new Promise<void>((resolve) => setImmediate(resolve));
