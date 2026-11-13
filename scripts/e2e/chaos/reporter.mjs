import { mkdirSync, writeFileSync } from "node:fs";
import { dirname } from "node:path";

/**
 * Opt-in chaos synchronization reporter. The setup project authenticates all
 * personas first; the first real test then records a marker consumed by the
 * Go driver, which applies the ChaosEngine. It is absent from normal runs.
 */
export default class ChaosMarkerReporter {
  constructor(options = {}) {
    this.markerFile = options.markerFile || process.env.E2E_CHAOS_MARKER_FILE;
    this.written = false;
  }

  onTestBegin(test) {
    if (this.written || !this.markerFile) return;
    const project = typeof test.project === "function" ? test.project().name : test.project?.name;
    if (project === "setup") return;
    mkdirSync(dirname(this.markerFile), { recursive: true });
    writeFileSync(
      this.markerFile,
      `${JSON.stringify({
        test: test.title,
        project: project || "unknown",
        runId: process.env.E2E_RUN_ID || "",
        scenario: process.env.CHAOS_SCENARIO || "",
      })}\n`,
      { flag: "wx" },
    );
    this.written = true;
  }
}
