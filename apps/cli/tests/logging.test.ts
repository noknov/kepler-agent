import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import test from "node:test";

test("logging remains safe when Ink redirects console.error into logError", () => {
  const logger = new URL("../src/cc/utils/log.ts", import.meta.url).href;
  const output = execFileSync(process.execPath, ["--import", "tsx", "--input-type=module", "-e", `
    import { format } from "node:util";
    import { logError } from ${JSON.stringify(logger)};
    let calls = 0;
    console.error = (...args) => {
      if (++calls > 1) throw new Error("recursive console logging");
      logError(new Error(format(...args)));
    };
    console.error("diagnostic probe");
    if (calls !== 1) throw new Error("logger re-entered console");
    process.stdout.write("ok");
  `], { encoding: "utf8", timeout: 5000, stdio: ["ignore", "pipe", "pipe"] });
  assert.equal(output, "ok");
});

test("a closed diagnostic descriptor does not break input handling", () => {
  const logger = new URL("../src/cc/utils/log.ts", import.meta.url).href;
  execFileSync(process.execPath, ["--import", "tsx", "--input-type=module", "-e", `
    import { closeSync } from "node:fs";
    import { logError } from ${JSON.stringify(logger)};
    closeSync(2);
    logError(new Error("closed stderr"));
  `], { timeout: 5000, stdio: "ignore" });
});
