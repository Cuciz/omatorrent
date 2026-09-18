#!/usr/bin/env python3
"""SessionStart hook: inject a one-line harness reminder.

Outputs {"additionalContext": "..."} on stdout (ZCode hook JSON contract).
Fails open: on any error it prints nothing and exits 0, so a broken hook can
never disturb a session.
"""
import json
import sys

REMINDER = (
    "OmaTorrent dev harness active. Read AGENTS.md before product work. "
    "Workflow commands: /ot-status /ot-plan /ot-implement /ot-review "
    "/ot-verify /ot-handoff /ot-release. Invariant: QML/Quickshell is "
    "presentation only - all business logic lives in omatorrent-service; "
    "qBittorrent is never contacted from QML."
)


def main() -> int:
    try:
        line = sys.stdin.readline()
        if line.strip():
            json.loads(line)  # validate; content is not needed here
    except Exception:
        pass  # a malformed event must not break session start
    try:
        sys.stdout.write(json.dumps({"additionalContext": REMINDER}) + "\n")
    except Exception:
        pass
    return 0


if __name__ == "__main__":
    sys.exit(main())
